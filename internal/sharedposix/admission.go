// Package sharedposix implements Stella's runtime admission gate for a
// qualified shared POSIX namespace.
package sharedposix

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const stateDir = ".stella-shared-posix"

var (
	ErrMissing       = errors.New("shared storage unavailable")
	ErrIdentity      = errors.New("shared storage identity mismatch")
	ErrQualification = errors.New("shared storage qualification mismatch")
	ErrReadOnly      = errors.New("shared storage is not writable")
	ErrStale         = errors.New("shared storage freshness is stale")
)

type Config struct {
	Root                string
	NamespaceIdentity   string
	QualificationSHA256 string
	WitnessID           string
	CheckInterval       time.Duration
	FreshnessTimeout    time.Duration
	StartupTimeout      time.Duration
}

type identityFile struct {
	NamespaceIdentity string `json:"namespace_identity"`
}

type witnessFile struct {
	ClientID string `json:"client_id"`
	Sequence uint64 `json:"sequence"`
}

// Admission is one process's fail-closed view of the shared mount contract.
// Check is cheap and is called by readiness and every Home capability admission;
// a background monitor performs the filesystem operations.
type Admission struct {
	cfg Config

	mu               sync.RWMutex
	err              error
	lastChecked      time.Time
	lastAdvance      time.Time
	sequence         uint64
	requireAdvance   bool
	recoveryBaseline bool

	rootInfo os.FileInfo
}

func New(ctx context.Context, cfg Config) (*Admission, error) {
	if cfg.Root == "" || cfg.NamespaceIdentity == "" || cfg.WitnessID == "" {
		return nil, errors.New("shared storage requires namespace identity, qualification digest, witness ID, and STELLA_HOME")
	}
	digest, err := hex.DecodeString(cfg.QualificationSHA256)
	if err != nil || len(digest) != sha256.Size {
		return nil, errors.New("shared storage qualification digest must be a SHA-256 hex value")
	}
	if cfg.CheckInterval <= 0 || cfg.FreshnessTimeout <= cfg.CheckInterval || cfg.StartupTimeout <= cfg.CheckInterval {
		return nil, errors.New("shared storage intervals must be positive and freshness/startup timeouts must exceed the check interval")
	}
	info, err := os.Lstat(cfg.Root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrMissing
	}
	a := &Admission{cfg: cfg, rootInfo: info, err: ErrStale}
	deadline := time.Now().Add(cfg.StartupTimeout)
	var initial uint64
	for {
		seq, checkErr := a.validateFilesystem()
		if checkErr != nil && !errors.Is(checkErr, ErrStale) {
			return nil, checkErr
		}
		if checkErr == nil && initial == 0 {
			initial = seq
		} else if checkErr == nil && seq > initial {
			now := time.Now()
			a.mu.Lock()
			a.sequence, a.lastAdvance, a.lastChecked, a.err = seq, now, now, nil
			a.mu.Unlock()
			break
		}
		if time.Now().Add(cfg.CheckInterval).After(deadline) {
			return nil, ErrStale
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(cfg.CheckInterval):
		}
	}
	go a.monitor(ctx)
	return a, nil
}

// Check reports only actionable, path-free contract errors. It never performs
// I/O, so request admission cannot hang on a disconnected mount.
func (a *Admission) Check(context.Context) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.err != nil {
		return a.err
	}
	if time.Since(a.lastChecked) > a.cfg.FreshnessTimeout || time.Since(a.lastAdvance) > a.cfg.FreshnessTimeout {
		return ErrStale
	}
	return nil
}

func (a *Admission) monitor(ctx context.Context) {
	ticker := time.NewTicker(a.cfg.CheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.refresh()
		}
	}
}

func (a *Admission) refresh() {
	seq, err := a.validateFilesystem()
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastChecked = now
	if err != nil {
		a.err = err
		a.requireAdvance = true
		a.recoveryBaseline = true
		return
	}
	if a.recoveryBaseline {
		if seq > a.sequence {
			a.sequence = seq
		}
		a.recoveryBaseline = false
		a.err = ErrStale
		return
	}
	if seq > a.sequence {
		a.sequence = seq
		a.lastAdvance = now
		a.requireAdvance = false
	}
	if a.requireAdvance || now.Sub(a.lastAdvance) > a.cfg.FreshnessTimeout {
		a.err = ErrStale
		a.requireAdvance = true
		return
	}
	// Recovery is deliberately a full validation: validateFilesystem has just
	// rechecked the root object, identity, qualification, write/fsync/read/remove,
	// and an advancing independent-client witness.
	a.err = nil
}

func (a *Admission) validateFilesystem() (uint64, error) {
	current, err := os.Lstat(a.cfg.Root)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(a.rootInfo, current) {
		return 0, ErrMissing
	}
	state, err := os.Lstat(filepath.Join(a.cfg.Root, stateDir))
	if err != nil || !state.IsDir() || state.Mode()&os.ModeSymlink != 0 {
		return 0, ErrMissing
	}
	identity, err := readBounded(filepath.Join(a.cfg.Root, stateDir, "identity.json"), 4<<10)
	if err != nil {
		return 0, ErrMissing
	}
	var id identityFile
	if json.Unmarshal(identity, &id) != nil || id.NamespaceIdentity != a.cfg.NamespaceIdentity {
		return 0, ErrIdentity
	}
	record, err := readBounded(filepath.Join(a.cfg.Root, stateDir, "qualification.json"), 4<<20)
	if err != nil {
		return 0, ErrQualification
	}
	sum := sha256.Sum256(record)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), a.cfg.QualificationSHA256) {
		return 0, ErrQualification
	}
	var qualification struct {
		NamespaceIdentity string `json:"namespace_identity"`
		QualifiedShared   bool   `json:"qualified_shared"`
		OverallPass       bool   `json:"overall_pass"`
	}
	if json.Unmarshal(record, &qualification) != nil || qualification.NamespaceIdentity != a.cfg.NamespaceIdentity || !qualification.QualifiedShared || !qualification.OverallPass {
		return 0, ErrQualification
	}
	witness, err := readBounded(filepath.Join(a.cfg.Root, stateDir, "witness.json"), 4<<10)
	if err != nil {
		return 0, ErrStale
	}
	var w witnessFile
	if json.Unmarshal(witness, &w) != nil || w.ClientID != a.cfg.WitnessID || w.Sequence == 0 {
		return 0, ErrStale
	}
	if err := a.writeProbe(); err != nil {
		return 0, ErrReadOnly
	}
	return w.Sequence, nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	expected, err := os.Lstat(path)
	if err != nil || !expected.Mode().IsRegular() || expected.Mode()&os.ModeSymlink != 0 || expected.Size() > limit {
		return nil, errors.New("invalid shared storage evidence file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit || !os.SameFile(expected, info) {
		_ = f.Close()
		return nil, errors.New("invalid shared storage evidence file")
	}
	data, readErr := io.ReadAll(io.LimitReader(f, limit+1))
	closeErr := f.Close()
	if int64(len(data)) > limit {
		return nil, errors.New("invalid shared storage evidence file")
	}
	return data, errors.Join(readErr, closeErr)
}

func (a *Admission) writeProbe() error {
	dir := filepath.Join(a.cfg.Root, stateDir, "probes")
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid shared storage probe directory")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("probe-%x", nonce[:]))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(path)
		}
	}()
	if _, err = f.Write(nonce[:]); err == nil {
		err = f.Sync()
	}
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	got := make([]byte, len(nonce))
	if err == nil {
		_, err = io.ReadFull(f, got)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if string(got) != string(nonce[:]) {
		return errors.New("shared storage probe read mismatch")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	remove = false
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	closeErr = d.Close()
	return errors.Join(err, closeErr)
}

// RunWitness publishes an advancing freshness witness from an independent
// client. The caller must ensure this process is not co-located with stellad.
func RunWitness(ctx context.Context, root, clientID string, interval time.Duration) error {
	if root == "" || clientID == "" || interval <= 0 {
		return errors.New("shared storage witness requires root, client ID, and a positive interval")
	}
	dir := filepath.Join(root, stateDir)
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrMissing
	}
	sequence := uint64(time.Now().UnixNano())
	write := func() error {
		sequence++
		data, _ := json.Marshal(witnessFile{ClientID: clientID, Sequence: sequence})
		tmp, err := os.CreateTemp(dir, ".witness-")
		if err != nil {
			return err
		}
		name := tmp.Name()
		defer func() { _ = os.Remove(name) }()
		if err = tmp.Chmod(0o600); err == nil {
			_, err = tmp.Write(data)
		}
		if err == nil {
			err = tmp.Sync()
		}
		if closeErr := tmp.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(name, filepath.Join(dir, "witness.json"))
		}
		if err != nil {
			return err
		}
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		err = d.Sync()
		return errors.Join(err, d.Close())
	}
	if err := write(); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := write(); err != nil {
				return err
			}
		}
	}
}
