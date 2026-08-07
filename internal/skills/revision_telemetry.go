package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

const (
	defaultRevisionScanDepth   = 16
	defaultRevisionScanEntries = 4096
	defaultRevisionScanBytes   = 128 << 20
	revisionWarningAgeDelta    = time.Hour
)

// RevisionScanLimits bounds one collection. Zero fields use the conservative
// defaults; callers may lower them for a smaller managed-revision ceiling.
type RevisionScanLimits struct {
	MaxDepth   int
	MaxEntries int
	MaxBytes   int64
}

// RevisionThresholds controls capacity warnings for each opaque catalog root.
// A zero threshold disables that dimension.
type RevisionThresholds struct {
	Count int64
	Bytes int64
}

// RevisionTelemetryConfig supplies only policy and observability dependencies.
// MeterProvider defaults to the process OTel provider, and Now defaults to UTC.
type RevisionTelemetryConfig struct {
	Limits        RevisionScanLimits
	Thresholds    RevisionThresholds
	MeterProvider metric.MeterProvider
	Logger        *slog.Logger
	Now           func() time.Time
}

type revisionObservation struct {
	count     int64
	bytes     int64
	oldestAge time.Duration
}

type revisionRootKey struct {
	scope, homeID, root string
}

type revisionRootState struct {
	generation uint64
	inFlight   int
}

// RevisionTelemetry keeps the latest complete scan per opaque catalog root.
// Metric callbacks read this cache only; they never touch a filesystem.
type RevisionTelemetry struct {
	mu         sync.RWMutex
	observed   map[revisionRootKey]revisionObservation
	breached   map[revisionRootKey]revisionObservation
	roots      map[revisionRootKey]*revisionRootState
	limits     RevisionScanLimits
	thresholds RevisionThresholds
	logger     *slog.Logger
	now        func() time.Time

	countGauge  metric.Int64ObservableGauge
	bytesGauge  metric.Int64ObservableGauge
	oldestGauge metric.Float64ObservableGauge
}

func NewRevisionTelemetry(config RevisionTelemetryConfig) (*RevisionTelemetry, error) {
	limits := config.Limits
	if limits.MaxDepth == 0 {
		limits.MaxDepth = defaultRevisionScanDepth
	}
	if limits.MaxEntries == 0 {
		limits.MaxEntries = defaultRevisionScanEntries
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = defaultRevisionScanBytes
	}
	if limits.MaxDepth < 0 || limits.MaxEntries < 0 || limits.MaxBytes < 0 {
		return nil, errors.New("skills: revision telemetry limits must not be negative")
	}
	if config.Thresholds.Count < 0 || config.Thresholds.Bytes < 0 {
		return nil, errors.New("skills: revision telemetry thresholds must not be negative")
	}
	provider := config.MeterProvider
	if provider == nil {
		provider = otel.GetMeterProvider()
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	t := &RevisionTelemetry{observed: make(map[revisionRootKey]revisionObservation), breached: make(map[revisionRootKey]revisionObservation), roots: make(map[revisionRootKey]*revisionRootState), limits: limits, thresholds: config.Thresholds, logger: logger, now: now}
	meter := provider.Meter("github.com/CherryHQ/stella/internal/skills")
	var err error
	if t.countGauge, err = meter.Int64ObservableGauge("stella.skill.revisions.count", metric.WithUnit("{revision}")); err != nil {
		return nil, fmt.Errorf("skills: create revision count gauge: %w", err)
	}
	if t.bytesGauge, err = meter.Int64ObservableGauge("stella.skill.revisions.bytes", metric.WithUnit("By")); err != nil {
		return nil, fmt.Errorf("skills: create revision bytes gauge: %w", err)
	}
	if t.oldestGauge, err = meter.Float64ObservableGauge("stella.skill.revisions.oldest_age", metric.WithUnit("s")); err != nil {
		return nil, fmt.Errorf("skills: create revision oldest-age gauge: %w", err)
	}
	if _, err := meter.RegisterCallback(t.observeMetrics, t.countGauge, t.bytesGauge, t.oldestGauge); err != nil {
		return nil, fmt.Errorf("skills: register revision metrics callback: %w", err)
	}
	return t, nil
}

// Observe scans one root synchronously. A failed collection leaves its prior
// snapshot intact and is not retried automatically.
func (t *RevisionTelemetry) Observe(ctx context.Context, filesystem sandbox.Filesystem, root FilesystemCatalogRoot) error {
	if t == nil {
		return errors.New("skills: revision telemetry is unavailable")
	}
	if filesystem == nil {
		return t.collectionFailed(root, errors.New("filesystem is required"))
	}
	key := revisionKey(root)
	t.mu.Lock()
	state := t.roots[key]
	if state == nil {
		state = &revisionRootState{}
		t.roots[key] = state
	}
	// Starting a newer scan invalidates every older in-flight result for this
	// root. The last successful cached snapshot remains available if it fails.
	state.generation++
	generation := state.generation
	state.inFlight++
	t.mu.Unlock()

	observation, err := scanRetainedRevisions(ctx, filesystem, root, t.limits, t.now().UTC())
	t.mu.Lock()
	current := t.roots[key] == state && state.generation == generation
	state.inFlight--
	if !current {
		t.cleanupRootLocked(key, state)
		t.mu.Unlock()
		return nil
	}
	if err != nil {
		t.cleanupRootLocked(key, state)
		t.mu.Unlock()
		return t.collectionFailed(root, err)
	}
	t.observed[key] = observation
	t.warnIfNeededLocked(root, key, observation)
	t.mu.Unlock()
	return nil
}

func (t *RevisionTelemetry) collectionFailed(root FilesystemCatalogRoot, err error) error {
	// Filesystem errors commonly embed a canonical path; log only a stable
	// category so collection failures cannot expose provider coordinates.
	t.logger.Warn("skill revision telemetry collection failed", "root_id", root.homeID, "scope", root.scope, "reason", revisionCollectionReason(err))
	return fmt.Errorf("skills: collect retained revisions: %w", err)
}

func revisionCollectionReason(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, fs.ErrNotExist):
		return "not_found"
	default:
		return "scan_failed"
	}
}

// Forget removes a deleted root from future metric aggregation and clears its
// warning state.
func (t *RevisionTelemetry) Forget(root FilesystemCatalogRoot) {
	if t == nil {
		return
	}
	key := revisionKey(root)
	t.mu.Lock()
	delete(t.observed, key)
	delete(t.breached, key)
	if state := t.roots[key]; state != nil {
		state.generation++
		t.cleanupRootLocked(key, state)
	}
	t.mu.Unlock()
}

// cleanupRootLocked drops invalidation state as soon as no scan and no cached
// observation needs it; Forget therefore cannot accumulate tombstones.
func (t *RevisionTelemetry) cleanupRootLocked(key revisionRootKey, state *revisionRootState) {
	if state.inFlight == 0 {
		if _, observed := t.observed[key]; !observed {
			if _, breached := t.breached[key]; !breached && t.roots[key] == state {
				delete(t.roots, key)
			}
		}
	}
}

func (t *RevisionTelemetry) observeMetrics(_ context.Context, observer metric.Observer) error {
	t.mu.RLock()
	aggregated := make(map[string]revisionObservation, 5)
	for key, observation := range t.observed {
		total := aggregated[key.scope]
		total.count += observation.count
		total.bytes += observation.bytes
		if observation.oldestAge > total.oldestAge {
			total.oldestAge = observation.oldestAge
		}
		aggregated[key.scope] = total
	}
	t.mu.RUnlock()
	for scope, observation := range aggregated {
		option := metric.WithAttributes(attribute.String("scope", scope))
		observer.ObserveInt64(t.countGauge, observation.count, option)
		observer.ObserveInt64(t.bytesGauge, observation.bytes, option)
		observer.ObserveFloat64(t.oldestGauge, observation.oldestAge.Seconds(), option)
	}
	return nil
}

func (t *RevisionTelemetry) warnIfNeededLocked(root FilesystemCatalogRoot, key revisionRootKey, observation revisionObservation) {
	breach := (t.thresholds.Count > 0 && observation.count >= t.thresholds.Count) || (t.thresholds.Bytes > 0 && observation.bytes >= t.thresholds.Bytes)
	previous, wasBreached := t.breached[key]
	if !breach {
		delete(t.breached, key)
		return
	}
	if wasBreached && previous.count == observation.count && previous.bytes == observation.bytes && absDuration(previous.oldestAge-observation.oldestAge) < revisionWarningAgeDelta {
		return
	}
	t.breached[key] = observation
	t.logger.Warn("skill retained revision capacity exceeded", "root_id", root.homeID, "scope", root.scope, "count", observation.count, "bytes", observation.bytes, "oldest_age", observation.oldestAge, "count_threshold", t.thresholds.Count, "bytes_threshold", t.thresholds.Bytes)
}

func scanRetainedRevisions(ctx context.Context, filesystem sandbox.Filesystem, root FilesystemCatalogRoot, limits RevisionScanLimits, now time.Time) (revisionObservation, error) {
	base := path.Join(root.root, ".stella-revisions")
	entries, err := filesystem.List(ctx, base)
	if errors.Is(err, fs.ErrNotExist) {
		return revisionObservation{}, nil
	}
	if err != nil {
		return revisionObservation{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	state := revisionScanState{limits: limits}
	var observation revisionObservation
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return revisionObservation{}, err
		}
		if err := state.consumeEntry(); err != nil {
			return revisionObservation{}, err
		}
		if err := skillNameValidationError(entry.Name, entry.Name); err != nil || !entry.IsDir || entry.Mode&fs.ModeSymlink != 0 || entry.Mode&fs.ModeType != fs.ModeDir {
			return revisionObservation{}, errors.New("invalid managed revision name directory")
		}
		namePath := path.Join(base, entry.Name)
		digests, err := filesystem.List(ctx, namePath)
		if err != nil {
			return revisionObservation{}, err
		}
		sort.Slice(digests, func(i, j int) bool { return digests[i].Name < digests[j].Name })
		for _, digest := range digests {
			if err := state.consumeEntry(); err != nil {
				return revisionObservation{}, err
			}
			if err := ctx.Err(); err != nil {
				return revisionObservation{}, err
			}
			if !validHomeSkillDigest(digest.Name) || !digest.IsDir || digest.Mode&fs.ModeSymlink != 0 || digest.Mode&fs.ModeType != fs.ModeDir {
				return revisionObservation{}, errors.New("invalid managed revision digest directory")
			}
			revisionPath := path.Join(namePath, digest.Name)
			info, err := filesystem.Stat(ctx, revisionPath)
			if err != nil {
				return revisionObservation{}, err
			}
			if !info.IsDir || info.Mode&fs.ModeSymlink != 0 || info.Mode&fs.ModeType != fs.ModeDir {
				return revisionObservation{}, errors.New("managed revision is not a real directory")
			}
			if err := state.walk(ctx, filesystem, revisionPath, "", 0, &observation); err != nil {
				return revisionObservation{}, err
			}
			age := now.Sub(info.ModTime.UTC())
			if age > observation.oldestAge {
				observation.oldestAge = age
			}
			observation.count++
		}
	}
	return observation, nil
}

type revisionScanState struct {
	limits  RevisionScanLimits
	entries int
	bytes   int64
}

func (s *revisionScanState) consumeEntry() error {
	if s.entries >= s.limits.MaxEntries {
		return errors.New("managed revision exceeds telemetry entry limit")
	}
	s.entries++
	return nil
}

func (s *revisionScanState) walk(ctx context.Context, filesystem sandbox.Filesystem, root, relative string, depth int, observation *revisionObservation) error {
	if depth > s.limits.MaxDepth {
		return errors.New("managed revision exceeds telemetry depth limit")
	}
	directory := root
	if relative != "" {
		directory = path.Join(root, relative)
	}
	entries, err := filesystem.List(ctx, directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.consumeEntry(); err != nil {
			return err
		}
		if entry.Name == "" || entry.Name == "." || entry.Name == ".." || strings.Contains(entry.Name, "/") {
			return errors.New("managed revision has unsafe entry path")
		}
		rel := entry.Name
		if relative != "" {
			rel = path.Join(relative, entry.Name)
		}
		if path.Clean(rel) != rel || strings.HasPrefix(rel, "../") {
			return errors.New("managed revision escapes telemetry root")
		}
		absolute := path.Join(root, rel)
		info, err := filesystem.Stat(ctx, absolute)
		if err != nil {
			return err
		}
		if info.Mode&fs.ModeSymlink != 0 || info.Mode&fs.ModeType != 0 && !info.IsDir {
			return errors.New("managed revision contains symlink or special file")
		}
		if entry.IsDir != info.IsDir || entry.Mode != info.Mode || (!info.IsDir && entry.Size != info.Size) {
			return errors.New("managed revision changed during telemetry collection")
		}
		if info.IsDir {
			if err := s.walk(ctx, filesystem, root, rel, depth+1, observation); err != nil {
				return err
			}
			continue
		}
		if info.Size < 0 || s.bytes > s.limits.MaxBytes-info.Size {
			return errors.New("managed revision exceeds telemetry byte limit")
		}
		s.bytes += info.Size
		observation.bytes += info.Size
	}
	return nil
}

func revisionKey(root FilesystemCatalogRoot) revisionRootKey {
	return revisionRootKey{scope: root.scope, homeID: root.homeID, root: root.root}
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}
