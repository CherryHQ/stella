package home

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/blob"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

const mutableAssetLayout = "principal_home_data_assets_v1"

// MutableAssetSource deliberately has no Put or Delete: the offline cutover
// can read the old authority but can never modify it.
type MutableAssetSource interface {
	List(context.Context, string) ([]string, error)
	Open(context.Context, string) (io.ReadCloser, error)
}

type MutableAssetMigrationOptions struct{ DryRun bool }

// MutableAssetMigrationSummary is stable command output, not an object-store
// inventory. The digest is the versioned aggregate over the source records.
type MutableAssetMigrationSummary struct {
	DryRun      bool   `json:"dry_run"`
	Status      string `json:"status"`
	MarkerState string `json:"marker_state"`
	Count       int64  `json:"count"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"sha256"`
}

type mutableAssetRecord struct {
	key      string
	owner    Key
	relative string
	digest   mutableAssetDigest
}

type mutableAssetMetadata struct {
	Layout       string `json:"layout"`
	SourceCount  int64  `json:"source_count"`
	SourceBytes  int64  `json:"source_bytes"`
	SourceSHA256 string `json:"source_sha256"`
	TargetCount  int64  `json:"target_count"`
	TargetBytes  int64  `json:"target_bytes"`
	TargetSHA256 string `json:"target_sha256"`
}

// MigrateMutableAssets performs the offline, idempotent mutable asset cutover.
// It has no remote mutation path; every durable write is through a typed local
// PrincipalHome and every source key is re-read before completion is recorded.
func (r *Registry) MigrateMutableAssets(ctx context.Context, source MutableAssetSource, options MutableAssetMigrationOptions) (summary MutableAssetMigrationSummary, err error) {
	if source == nil {
		return summary, errors.New("home: mutable asset migration source is required")
	}
	marker, err := r.mutableAssetMigrationMarker(ctx, options.DryRun)
	if err != nil {
		return summary, err
	}
	if err := validateMutableAssetMigrationMarker(marker); err != nil {
		return summary, err
	}
	owners, err := r.mutableAssetOwners(ctx)
	if err != nil {
		return summary, err
	}
	records, err := listMutableAssetRecords(ctx, source, owners)
	if err != nil {
		return summary, err
	}
	if options.DryRun {
		records, err = r.inspectMutableAssetDryRun(ctx, source, records)
		if err != nil {
			return summary, err
		}
		count, bytes, digest := aggregateMutableAssets(records)
		return MutableAssetMigrationSummary{DryRun: true, Status: "planned", MarkerState: marker.State, Count: count, Bytes: bytes, SHA256: digest}, nil
	}

	published := false
	fail := func(cause error) error {
		if published && !errors.Is(cause, sandbox.ErrOutcomeUnknown) {
			return fmt.Errorf("%w: mutable asset target may have been published: %w", sandbox.ErrOutcomeUnknown, cause)
		}
		return cause
	}
	for i := range records {
		if err := ctx.Err(); err != nil {
			return summary, fail(err)
		}
		home, ensureErr := r.Ensure(ctx, records[i].owner)
		if ensureErr != nil {
			return summary, fail(fmt.Errorf("ensure mutable asset Home: %w", ensureErr))
		}
		store, storeErr := r.mutableAssetStore(home)
		if storeErr != nil {
			return summary, fail(storeErr)
		}
		reader, openErr := source.Open(ctx, records[i].key)
		if openErr != nil {
			return summary, fail(fmt.Errorf("open mutable asset source %q: %w", records[i].key, openErr))
		}
		digest, didPublish, installErr := store.installMutableAsset(ctx, home, records[i].relative, reader)
		published = published || didPublish
		if installErr != nil {
			return summary, fail(fmt.Errorf("install mutable asset %q: %w", records[i].key, installErr))
		}
		records[i].digest = digest
	}

	verifiedSource, verifiedTarget, verifyErr := r.verifyMutableAssetTargets(ctx, source, records)
	if verifyErr != nil {
		return summary, fail(verifyErr)
	}
	sourceCount, sourceBytes, sourceHash := aggregateMutableAssets(verifiedSource)
	targetCount, targetBytes, targetHash := aggregateMutableAssets(verifiedTarget)
	metadata := mutableAssetMetadata{
		Layout: mutableAssetLayout, SourceCount: sourceCount, SourceBytes: sourceBytes, SourceSHA256: sourceHash,
		TargetCount: targetCount, TargetBytes: targetBytes, TargetSHA256: targetHash,
	}
	if sourceCount != targetCount || sourceBytes != targetBytes || sourceHash != targetHash {
		return summary, fail(errors.New("home: mutable asset source and target aggregate differ"))
	}
	if marker.State == "completed" {
		if !mutableAssetMetadataMatches(marker.Metadata, metadata) {
			return summary, fail(errors.New("home: completed mutable asset migration metadata differs from source"))
		}
		return MutableAssetMigrationSummary{Status: "completed", MarkerState: "completed", Count: sourceCount, Bytes: sourceBytes, SHA256: sourceHash}, nil
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return summary, fail(fmt.Errorf("encode mutable asset migration metadata: %w", err))
	}
	if _, err = r.q.CompleteMutableAssetStorageMigration(ctx, sqlc.CompleteMutableAssetStorageMigrationParams{Name: MutableAssetObjectAuthorityMigration, Metadata: encoded}); err == nil {
		return MutableAssetMigrationSummary{Status: "completed", MarkerState: "completed", Count: sourceCount, Bytes: sourceBytes, SHA256: sourceHash}, nil
	}
	// A CAS miss and a connection loss can look identical to the caller. Reload
	// once; only the exact durable completion proves a successful outcome.
	reloaded, reloadErr := r.q.GetStorageMigration(ctx, MutableAssetObjectAuthorityMigration)
	if reloadErr == nil && reloaded.State == "completed" && reloaded.ObjectAuthorityConfigured && mutableAssetMetadataMatches(reloaded.Metadata, metadata) {
		return MutableAssetMigrationSummary{Status: "completed", MarkerState: "completed", Count: sourceCount, Bytes: sourceBytes, SHA256: sourceHash}, nil
	}
	if reloadErr != nil {
		return summary, fail(fmt.Errorf("%w: complete mutable asset migration: %w; reload marker: %w", sandbox.ErrOutcomeUnknown, err, reloadErr))
	}
	return summary, fail(fmt.Errorf("%w: mutable asset migration completion lost to state %q", sandbox.ErrOutcomeUnknown, reloaded.State))
}

func (r *Registry) mutableAssetMigrationMarker(ctx context.Context, dryRun bool) (sqlc.StorageMigration, error) {
	marker, err := r.q.GetStorageMigration(ctx, MutableAssetObjectAuthorityMigration)
	if err == nil {
		if dryRun && marker.State == "not_required" && !marker.ObjectAuthorityConfigured {
			// A restored legacy authority must be inspectable before an operator
			// commits to the durable observation transition.
			marker.State = "pending"
			marker.ObjectAuthorityConfigured = true
			return marker, nil
		}
		if !dryRun && marker.State == "not_required" && !marker.ObjectAuthorityConfigured {
			if observeErr := r.ObserveMutableAssetObjectAuthority(ctx, true); observeErr != nil {
				return sqlc.StorageMigration{}, observeErr
			}
			marker, err = r.q.GetStorageMigration(ctx, MutableAssetObjectAuthorityMigration)
		}
		if err != nil {
			return sqlc.StorageMigration{}, fmt.Errorf("home: reload mutable asset migration marker: %w", err)
		}
		return marker, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return sqlc.StorageMigration{}, fmt.Errorf("home: load mutable asset migration marker: %w", err)
	}
	if dryRun {
		return sqlc.StorageMigration{Name: MutableAssetObjectAuthorityMigration, State: "pending", ObjectAuthorityConfigured: true}, nil
	}
	marker, err = r.q.CreateStorageMigrationObservation(ctx, sqlc.CreateStorageMigrationObservationParams{Name: MutableAssetObjectAuthorityMigration, State: "pending", ObjectAuthorityConfigured: true, Metadata: []byte(`{}`)})
	if err == nil {
		return marker, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return sqlc.StorageMigration{}, fmt.Errorf("home: create mutable asset migration marker: %w", err)
	}
	marker, err = r.q.GetStorageMigration(ctx, MutableAssetObjectAuthorityMigration)
	if err != nil {
		return sqlc.StorageMigration{}, fmt.Errorf("home: reload mutable asset migration marker: %w", err)
	}
	return marker, nil
}

func validateMutableAssetMigrationMarker(marker sqlc.StorageMigration) error {
	if marker.Name != MutableAssetObjectAuthorityMigration {
		return errors.New("home: corrupt mutable asset migration marker")
	}
	if !marker.ObjectAuthorityConfigured {
		return errors.New("home: mutable asset migration requires legacy object authority configuration")
	}
	if marker.State != "pending" && marker.State != "completed" {
		return fmt.Errorf("home: mutable asset migration has invalid state %q", marker.State)
	}
	return nil
}

func (r *Registry) mutableAssetOwners(ctx context.Context) (map[string]Key, error) {
	users, err := r.q.ListStorageLegacyUserID(ctx)
	if err != nil {
		return nil, fmt.Errorf("home: list mutable asset users: %w", err)
	}
	groups, err := r.q.ListStorageLegacyGroupID(ctx)
	if err != nil {
		return nil, fmt.Errorf("home: list mutable asset groups: %w", err)
	}
	return mutableAssetOwnerMap(users, groups)
}

func mutableAssetOwnerMap(users, groups []string) (map[string]Key, error) {
	users = append([]string(nil), users...)
	groups = append([]string(nil), groups...)
	sort.Strings(users)
	sort.Strings(groups)
	owners := make(map[string]Key, len(users)+len(groups))
	add := func(token string, key Key) error {
		if err := key.Validate(); err != nil {
			return err
		}
		if previous, exists := owners[token]; exists {
			return fmt.Errorf("home: ambiguous mutable asset owner token %q (%s/%s and %s/%s)", token, previous.PrincipalKind, previous.PrincipalID, key.PrincipalKind, key.PrincipalID)
		}
		owners[token] = key
		return nil
	}
	for _, id := range users {
		if err := add(id, Principal(UserPrincipal, id)); err != nil {
			return nil, err
		}
	}
	for _, id := range groups {
		if err := add("group-"+id, Principal(GroupPrincipal, id)); err != nil {
			return nil, err
		}
	}
	return owners, nil
}

func listMutableAssetRecords(ctx context.Context, source MutableAssetSource, owners map[string]Key) ([]mutableAssetRecord, error) {
	keys, err := source.List(ctx, "users")
	if err != nil {
		return nil, fmt.Errorf("list mutable asset source: %w", err)
	}
	sort.Strings(keys)
	records := make([]mutableAssetRecord, 0, len(keys))
	for i, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if i > 0 && key == keys[i-1] {
			return nil, fmt.Errorf("home: duplicate mutable asset source key %q", key)
		}
		validated, validateErr := blob.ValidateKey(key)
		if validateErr != nil || validated != key {
			return nil, fmt.Errorf("home: mutable asset source returned non-canonical key %q", key)
		}
		record, assetShaped, parseErr := parseMutableAssetKey(key, owners)
		if parseErr != nil {
			return nil, parseErr
		}
		if assetShaped {
			records = append(records, record)
		}
	}
	return records, nil
}

func parseMutableAssetKey(key string, owners map[string]Key) (mutableAssetRecord, bool, error) {
	parts := strings.Split(key, "/")
	assetShaped := strings.HasPrefix(key, "users/") && slices.Contains(parts, "assets")
	if len(parts) >= 4 && parts[0] == "users" && parts[2] == "data" && parts[3] == "assets" {
		if len(parts) < 5 {
			return mutableAssetRecord{}, true, fmt.Errorf("home: malformed mutable asset key %q", key)
		}
		for _, part := range parts {
			if part == "" || part == "." || part == ".." || strings.Contains(part, `\`) {
				return mutableAssetRecord{}, true, fmt.Errorf("home: malformed mutable asset key %q", key)
			}
		}
		owner, known := owners[parts[1]]
		if !known {
			return mutableAssetRecord{}, true, fmt.Errorf("home: mutable asset key %q has unknown owner %q", key, parts[1])
		}
		relative := strings.Join(parts[4:], "/")
		if err := validateMutableAssetRelative(relative); err != nil {
			return mutableAssetRecord{}, true, fmt.Errorf("home: malformed mutable asset key %q: %w", key, err)
		}
		return mutableAssetRecord{key: key, owner: owner, relative: relative}, true, nil
	}
	if assetShaped {
		return mutableAssetRecord{}, true, fmt.Errorf("home: malformed mutable asset key %q", key)
	}
	return mutableAssetRecord{}, false, nil
}

func (r *Registry) inspectMutableAssetDryRun(ctx context.Context, source MutableAssetSource, records []mutableAssetRecord) ([]mutableAssetRecord, error) {
	store := r.stores[r.defaultStore]
	migrationStore, ok := store.(mutableAssetStore)
	if !ok {
		return nil, fmt.Errorf("home: Store %q cannot inspect mutable assets", store.ID())
	}
	for i := range records {
		locator, err := store.Allocate(records[i].owner)
		if err != nil {
			return nil, err
		}
		home := Record{Key: records[i].owner, StoreID: store.ID(), Locator: locator}
		digest, err := digestMutableAssetSource(ctx, source, records[i].key)
		if err != nil {
			return nil, err
		}
		records[i].digest = digest
		if target, inspectErr := migrationStore.inspectMutableAsset(ctx, home, records[i].relative); inspectErr == nil && target != digest {
			return nil, fmt.Errorf("home: dry-run target differs for %q", records[i].key)
		} else if inspectErr != nil && !errors.Is(inspectErr, os.ErrNotExist) {
			return nil, fmt.Errorf("home: inspect dry-run target %q: %w", records[i].key, inspectErr)
		}
	}
	return records, nil
}

func (r *Registry) verifyMutableAssetTargets(ctx context.Context, source MutableAssetSource, records []mutableAssetRecord) ([]mutableAssetRecord, []mutableAssetRecord, error) {
	// Relist before reading so a disappeared, added, duplicate, or malformed key
	// cannot be hidden behind the first inventory.
	owners, err := r.mutableAssetOwners(ctx)
	if err != nil {
		return nil, nil, err
	}
	fresh, err := listMutableAssetRecords(ctx, source, owners)
	if err != nil {
		return nil, nil, err
	}
	if len(fresh) != len(records) {
		return nil, nil, errors.New("home: mutable asset source changed during migration")
	}
	targets := make([]mutableAssetRecord, len(fresh))
	for i := range fresh {
		if fresh[i].key != records[i].key || fresh[i].owner != records[i].owner || fresh[i].relative != records[i].relative {
			return nil, nil, errors.New("home: mutable asset source changed during migration")
		}
		digest, err := digestMutableAssetSource(ctx, source, fresh[i].key)
		if err != nil {
			return nil, nil, err
		}
		fresh[i].digest = digest
		home, err := r.Ensure(ctx, fresh[i].owner)
		if err != nil {
			return nil, nil, err
		}
		store, err := r.mutableAssetStore(home)
		if err != nil {
			return nil, nil, err
		}
		target, err := store.inspectMutableAsset(ctx, home, fresh[i].relative)
		if err != nil {
			return nil, nil, fmt.Errorf("home: verify mutable asset target %q: %w", fresh[i].key, err)
		}
		if target != digest {
			return nil, nil, fmt.Errorf("home: mutable asset target differs for %q", fresh[i].key)
		}
		if err := store.syncMutableAsset(ctx, home, fresh[i].relative); err != nil {
			return nil, nil, fmt.Errorf("home: sync mutable asset target %q: %w", fresh[i].key, err)
		}
		targets[i] = fresh[i]
		targets[i].digest = target
	}
	return fresh, targets, nil
}

func (r *Registry) mutableAssetStore(record Record) (mutableAssetStore, error) {
	store := r.stores[record.StoreID]
	migrationStore, ok := store.(mutableAssetStore)
	if !ok {
		return nil, fmt.Errorf("home: Store %q cannot migrate mutable assets", record.StoreID)
	}
	return migrationStore, nil
}

func digestMutableAssetSource(ctx context.Context, source MutableAssetSource, key string) (mutableAssetDigest, error) {
	reader, err := source.Open(ctx, key)
	if err != nil {
		return mutableAssetDigest{}, err
	}
	digest, readErr := copyDigest(ctx, io.Discard, reader)
	closeErr := reader.Close()
	if readErr != nil {
		return mutableAssetDigest{}, errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return mutableAssetDigest{}, closeErr
	}
	return digest, nil
}

func aggregateMutableAssets(records []mutableAssetRecord) (int64, int64, string) {
	ordered := append([]mutableAssetRecord(nil), records...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].key < ordered[j].key })
	hash := sha256.New()
	_, _ = hash.Write([]byte("stella-mutable-assets-v1\x00"))
	var count, bytes int64
	var encoded [8]byte
	for _, record := range ordered {
		binary.BigEndian.PutUint64(encoded[:], uint64(len(record.key)))
		_, _ = hash.Write(encoded[:])
		_, _ = hash.Write([]byte(record.key))
		binary.BigEndian.PutUint64(encoded[:], uint64(record.digest.Size))
		_, _ = hash.Write(encoded[:])
		_, _ = hash.Write(record.digest.SHA256[:])
		count++
		bytes += record.digest.Size
	}
	return count, bytes, hex.EncodeToString(hash.Sum(nil))
}

func mutableAssetMetadataMatches(raw []byte, expected mutableAssetMetadata) bool {
	var actual mutableAssetMetadata
	return json.Unmarshal(raw, &actual) == nil && actual == expected
}

func validMutableAssetMetadata(raw []byte) bool {
	var metadata mutableAssetMetadata
	if json.Unmarshal(raw, &metadata) != nil || metadata.Layout != mutableAssetLayout ||
		metadata.SourceCount < 0 || metadata.SourceBytes < 0 || metadata.TargetCount < 0 || metadata.TargetBytes < 0 ||
		metadata.SourceCount != metadata.TargetCount || metadata.SourceBytes != metadata.TargetBytes || metadata.SourceSHA256 != metadata.TargetSHA256 {
		return false
	}
	for _, digest := range []string{metadata.SourceSHA256, metadata.TargetSHA256} {
		if len(digest) != sha256.Size*2 {
			return false
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return false
		}
	}
	return true
}
