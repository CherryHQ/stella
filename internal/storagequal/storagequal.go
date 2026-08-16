// Package storagequal qualifies two independently mounted views of a POSIX
// namespace. It is intentionally backend-neutral and has no admission side
// effects; operators persist and review the returned Record themselves.
package storagequal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Metadata struct {
	Backend           string   `json:"backend"`
	Version           string   `json:"version"`
	Topology          string   `json:"topology"`
	Clients           int      `json:"clients"`
	Nodes             int      `json:"nodes"`
	MountOptions      []string `json:"mount_options"`
	NamespaceIdentity string   `json:"namespace_identity"`
	IdentityMechanism string   `json:"identity_mechanism"`
	ReferenceHardware string   `json:"reference_hardware"`
	IndependentMounts bool     `json:"independent_mounts"`
}

type Limits struct {
	MetadataP95MS      float64 `json:"metadata_p95_ms"`
	SmallFilesP95MS    float64 `json:"small_files_p95_ms"`
	ConcurrentP95MS    float64 `json:"concurrent_p95_ms"`
	StreamMiBPerSecond float64 `json:"stream_mib_per_second"`
	MinimumFreeBytes   int64   `json:"minimum_free_bytes"`
}

type Config struct {
	ClientA          string            `json:"client_a"`
	ClientB          string            `json:"client_b"`
	Metadata         Metadata          `json:"metadata"`
	Limits           Limits            `json:"limits"`
	FaultInjector    FaultInjector     `json:"-"`
	FailureInjection *FailureInjection `json:"failure_injection"`
	Now              func() time.Time  `json:"-"`
}

type FaultInjector interface {
	Inject(context.Context, string, string) FailureInjection
}

type FailureInjection struct {
	Injected           bool   `json:"injected"`
	DisconnectObserved bool   `json:"disconnect_observed"`
	Remounted          bool   `json:"remounted"`
	Revalidated        bool   `json:"revalidated"`
	ErrorClass         string `json:"error_class"`
	OutcomeUnknown     bool   `json:"outcome_unknown"`
	Detail             string `json:"detail,omitempty"`
}

type Result struct {
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
	Passed bool   `json:"passed"`
}

type Benchmark struct {
	Name       string  `json:"name"`
	Unit       string  `json:"unit"`
	Value      float64 `json:"value"`
	Criterion  float64 `json:"criterion"`
	Comparison string  `json:"comparison"`
	Passed     bool    `json:"passed"`
}

type Transition struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type Record struct {
	SchemaVersion     int              `json:"schema_version"`
	StartedUTC        string           `json:"started_utc"`
	EndedUTC          string           `json:"ended_utc"`
	Backend           string           `json:"backend"`
	BackendVersion    string           `json:"backend_version"`
	Topology          string           `json:"topology"`
	Clients           int              `json:"clients"`
	Nodes             int              `json:"nodes"`
	MountOptions      []string         `json:"mount_options"`
	NamespaceIdentity string           `json:"namespace_identity"`
	IdentityMechanism string           `json:"identity_mechanism"`
	ReferenceHardware string           `json:"reference_hardware"`
	IndependentMounts bool             `json:"independent_mounts"`
	IdentityEvidence  []Result         `json:"identity_evidence"`
	Conformance       []Result         `json:"conformance"`
	Benchmarks        []Benchmark      `json:"benchmarks"`
	Limits            Limits           `json:"limits"`
	FailureInjection  FailureInjection `json:"failure_injection"`
	Readiness         []Transition     `json:"readiness"`
	Recovery          Result           `json:"recovery"`
	QualifiedShared   bool             `json:"qualified_shared"`
	OverallPass       bool             `json:"overall_pass"`
}

// JSON returns stable compact JSON. Record uses only structs and ordered slices;
// mount options are normalized by Run.
func (r Record) JSON() ([]byte, error) { return json.Marshal(r) }

func Run(ctx context.Context, cfg Config) (Record, error) {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	r := Record{SchemaVersion: 1, StartedUTC: now().UTC().Format(time.RFC3339Nano), Backend: cfg.Metadata.Backend, BackendVersion: cfg.Metadata.Version, Topology: cfg.Metadata.Topology, Clients: cfg.Metadata.Clients, Nodes: cfg.Metadata.Nodes, NamespaceIdentity: cfg.Metadata.NamespaceIdentity, IdentityMechanism: cfg.Metadata.IdentityMechanism, ReferenceHardware: cfg.Metadata.ReferenceHardware, IndependentMounts: cfg.Metadata.IndependentMounts, Limits: cfg.Limits}
	r.MountOptions = append([]string(nil), cfg.Metadata.MountOptions...)
	sort.Strings(r.MountOptions)
	r.Readiness = append(r.Readiness, Transition{"not_ready", "qualification_started"})
	if err := validate(cfg); err != nil {
		r.EndedUTC = now().UTC().Format(time.RFC3339Nano)
		return r, err
	}
	absA, errA := filepath.Abs(cfg.ClientA)
	absB, errB := filepath.Abs(cfg.ClientB)
	rootA, statA := os.Lstat(cfg.ClientA)
	rootB, statB := os.Lstat(cfg.ClientB)
	// Two mount points into one shared namespace commonly resolve to the same
	// root inode. Distinct evidence means two real, non-symlink mount paths plus
	// the operator's independently mounted topology declaration; inode equality
	// is proved separately by the cross-client probe below.
	distinctPaths := errA == nil && errB == nil && statA == nil && statB == nil && absA != absB && rootA.IsDir() && rootB.IsDir() && rootA.Mode()&os.ModeSymlink == 0 && rootB.Mode()&os.ModeSymlink == 0
	work := ".stella-storagequal-" + strings.ReplaceAll(cfg.Metadata.NamespaceIdentity, "/", "_")
	a, b := filepath.Join(cfg.ClientA, work), filepath.Join(cfg.ClientB, work)
	_ = os.RemoveAll(a)
	_ = os.RemoveAll(b)
	if err := os.Mkdir(a, 0o700); err != nil {
		return finishError(&r, now, fmt.Errorf("create probe through client A: %w", err))
	}
	defer func() { _ = os.RemoveAll(a); _ = os.RemoveAll(b) }()
	if stA, e1 := os.Stat(a); e1 != nil {
		return finishError(&r, now, e1)
	} else if stB, e2 := os.Stat(b); e2 != nil || !sameNamespaceObject(stA, stB) {
		r.IdentityEvidence = []Result{{Name: "same_namespace_inode", Detail: "probe directory is not the same object through both clients", Passed: false}}
		return finishError(&r, now, errors.New("storagequal: roots do not prove one namespace"))
	}
	r.IdentityEvidence = []Result{
		{Name: "same_namespace_inode", Detail: "cross-client probe resolves to the same filesystem object", Passed: true},
		{Name: "distinct_client_root_objects", Detail: "aliasing one local root is not evidence of independent clients", Passed: distinctPaths},
		{Name: "declared_independent_mounts", Detail: "operator topology assertion; identity tests alone do not prove a shared backend", Passed: cfg.Metadata.IndependentMounts},
	}
	r.Conformance = runConformance(ctx, a, b)
	r.Benchmarks = runBenchmarks(ctx, a, b, cfg.Limits)
	if cfg.FaultInjector != nil {
		r.FailureInjection = cfg.FaultInjector.Inject(ctx, a, b)
	} else {
		r.FailureInjection = *cfg.FailureInjection
	}
	faultPass := r.FailureInjection.Injected && r.FailureInjection.DisconnectObserved && r.FailureInjection.OutcomeUnknown && r.FailureInjection.ErrorClass == "outcome_unknown" && r.FailureInjection.Remounted && r.FailureInjection.Revalidated && r.FailureInjection.Detail != ""
	r.Readiness = append(r.Readiness, Transition{"not_ready", "fault_injected"})
	r.Recovery = Result{Name: "full_revalidation_after_remount", Detail: r.FailureInjection.Detail, Passed: faultPass}
	all := faultPass && cfg.Metadata.IndependentMounts && distinctPaths
	for _, x := range r.Conformance {
		all = all && x.Passed
	}
	for _, x := range r.Benchmarks {
		all = all && x.Passed
	}
	r.QualifiedShared = all
	r.OverallPass = all
	if all {
		r.Readiness = append(r.Readiness, Transition{"ready", "full_contract_validated"})
	} else {
		r.Readiness = append(r.Readiness, Transition{"not_ready", "qualification_failed"})
	}
	r.EndedUTC = now().UTC().Format(time.RFC3339Nano)
	return r, nil
}

func validate(c Config) error {
	if c.ClientA == "" || c.ClientB == "" || c.Metadata.Backend == "" || c.Metadata.Version == "" || c.Metadata.Topology == "" || len(c.Metadata.MountOptions) == 0 || c.Metadata.NamespaceIdentity == "" || c.Metadata.IdentityMechanism == "" || c.Metadata.ReferenceHardware == "" || c.Metadata.Clients < 2 || c.Metadata.Nodes < 1 {
		return errors.New("storagequal: incomplete roots or operator metadata")
	}
	l := c.Limits
	if l.MetadataP95MS <= 0 || l.SmallFilesP95MS <= 0 || l.ConcurrentP95MS <= 0 || l.StreamMiBPerSecond <= 0 || l.MinimumFreeBytes <= 0 {
		return errors.New("storagequal: every benchmark criterion must be declared before execution")
	}
	if c.FaultInjector == nil && c.FailureInjection == nil {
		return errors.New("storagequal: failure injection evidence or injector is required")
	}
	return nil
}

func finishError(r *Record, now func() time.Time, err error) (Record, error) {
	r.Readiness = append(r.Readiness, Transition{"not_ready", "identity_or_write_check_failed"})
	r.EndedUTC = now().UTC().Format(time.RFC3339Nano)
	return *r, err
}

func check(name string, fn func() error) Result {
	if err := fn(); err != nil {
		return Result{name, err.Error(), false}
	}
	return Result{name, "", true}
}

func runConformance(ctx context.Context, a, b string) []Result {
	return []Result{
		check("atomic_same_directory_rename", func() error {
			p := filepath.Join(a, "old")
			if err := os.WriteFile(p, []byte("complete"), 0o600); err != nil {
				return err
			}
			if err := os.Rename(p, filepath.Join(a, "new")); err != nil {
				return err
			}
			v, e := os.ReadFile(filepath.Join(b, "new"))
			if e != nil || string(v) != "complete" {
				return errors.New("renamed value not atomically visible")
			}
			return nil
		}),
		check("symlink_and_containment", func() error {
			if err := os.Symlink("new", filepath.Join(a, "link")); err != nil {
				return err
			}
			v, e := os.ReadFile(filepath.Join(b, "link"))
			if e != nil || string(v) != "complete" {
				return errors.New("safe relative symlink failed")
			}
			if err := os.Symlink("../escape", filepath.Join(a, "escape")); err != nil {
				return err
			}
			target, e := filepath.EvalSymlinks(filepath.Join(b, "escape"))
			if e == nil && strings.HasPrefix(target, b+string(os.PathSeparator)) {
				return errors.New("escaping symlink incorrectly remained contained")
			}
			return nil
		}),
		check("modes_and_ownership", func() error {
			p := filepath.Join(a, "mode")
			if err := os.WriteFile(p, []byte("x"), 0o640); err != nil {
				return err
			}
			sa, e := os.Stat(p)
			if e != nil {
				return e
			}
			sb, e := os.Stat(filepath.Join(b, "mode"))
			if e != nil {
				return e
			}
			if sa.Mode().Perm() != 0o640 || sb.Mode().Perm() != 0o640 || !sameOwner(sa, sb) {
				return errors.New("mode or ownership differs")
			}
			return nil
		}),
		check("advisory_lock_across_clients", func() error { return lockTest(filepath.Join(a, "lock"), filepath.Join(b, "lock")) }),
		check("atomic_append", func() error { return appendTest(filepath.Join(a, "append"), filepath.Join(b, "append")) }),
		check("concurrent_read_write_no_torn_records", func() error { return tornTest(ctx, filepath.Join(a, "records"), filepath.Join(b, "records")) }),
		check("close_to_open_a_to_b", func() error { return visible(filepath.Join(a, "vis-ab"), filepath.Join(b, "vis-ab")) }),
		check("close_to_open_b_to_a", func() error { return visible(filepath.Join(b, "vis-ba"), filepath.Join(a, "vis-ba")) }),
		check("fsync_file_and_directory", func() error { return syncFileDir(a, b) }),
	}
}

func visible(write, read string) error {
	if err := os.WriteFile(write, []byte("visible"), 0o600); err != nil {
		return err
	}
	v, e := os.ReadFile(read)
	if e != nil || string(v) != "visible" {
		return errors.New("closed write not visible on peer open")
	}
	return nil
}

func appendTest(a, b string) error {
	// Stella requires each O_APPEND write to preserve the preceding closed
	// cross-client value and one record. It does not use lock-free simultaneous
	// append from different clients; global advisory locking is tested above.
	for i := range 128 {
		path := a
		flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
		if i%2 == 1 {
			path = b
			flags = os.O_WRONLY | os.O_APPEND
		}
		f, err := os.OpenFile(path, flags, 0o600)
		if err != nil {
			return err
		}
		_, writeErr := f.Write([]byte("0123456789abcdef\n"))
		if closeErr := f.Close(); writeErr == nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			return writeErr
		}
	}
	v, e := os.ReadFile(a)
	if e != nil {
		return e
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(v)), "\n") {
		if line != "0123456789abcdef" {
			return errors.New("torn append record")
		}
	}
	if len(strings.Split(strings.TrimSpace(string(v)), "\n")) != 128 {
		return errors.New("append records lost")
	}
	return nil
}

func tornTest(ctx context.Context, a, b string) error {
	const one = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"
	const two = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB\n"
	if err := os.WriteFile(a, []byte(one), 0o600); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		for i := range 200 {
			value := one
			if i%2 == 1 {
				value = two
			}
			tmp := fmt.Sprintf("%s-%d", a, i)
			if err := os.WriteFile(tmp, []byte(value), 0o600); err != nil {
				done <- err
				return
			}
			if err := os.Rename(tmp, a); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	reads := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			if err != nil {
				return err
			}
			if reads == 0 {
				return errors.New("concurrent reader made no progress")
			}
			return nil
		default:
		}
		v, e := os.ReadFile(b)
		if e != nil {
			return e
		}
		if string(v) != one && string(v) != two {
			return errors.New("reader observed torn record")
		}
		reads++
	}
}

func runBenchmarks(ctx context.Context, a, b string, l Limits) []Benchmark {
	lat := func(name string, limit float64, fn func(int) error) Benchmark {
		samples := make([]float64, 20)
		ok := true
		for i := range samples {
			start := time.Now()
			if ctx.Err() != nil || fn(i) != nil {
				ok = false
			}
			samples[i] = float64(time.Since(start).Microseconds()) / 1000
		}
		sort.Float64s(samples)
		v := samples[18]
		return Benchmark{name, "ms_p95", v, limit, "less_or_equal", ok && v <= limit}
	}
	meta := lat("typed_root_metadata_traversal", l.MetadataP95MS, func(sample int) error {
		rel := filepath.Join("typed", fmt.Sprintf("sample-%02d", sample), "users", "principal", "agents", "one", "workspace")
		p := filepath.Join(a, rel)
		if err := os.MkdirAll(p, 0o700); err != nil {
			return err
		}
		for current, stop := p, filepath.Join(a, "typed"); ; current = filepath.Dir(current) {
			if err := syncDirectory(current); err != nil {
				return err
			}
			if current == stop {
				break
			}
		}
		_, err := os.Stat(filepath.Join(b, rel))
		return err
	})
	small := lat("small_file_project_skill_publication", l.SmallFilesP95MS, func(sample int) error {
		name := fmt.Sprintf("revision-%02d", sample)
		if err := durableDirectoryPublication(filepath.Join(a, "skills"), name, 16); err != nil {
			return err
		}
		entries, err := os.ReadDir(filepath.Join(b, "skills", name))
		if err != nil || len(entries) != 16 {
			return errors.New("published Skill revision is incomplete on peer")
		}
		for _, entry := range entries {
			value, readErr := os.ReadFile(filepath.Join(b, "skills", name, entry.Name()))
			if readErr != nil || string(value) != "skill" {
				return errors.New("published Skill file is incomplete on peer")
			}
		}
		return nil
	})
	buf := make([]byte, 4<<20)
	start := time.Now()
	err := durableFilePublication(a, "upload", buf)
	if err == nil {
		f, e := os.Open(filepath.Join(b, "upload"))
		if e == nil {
			var copied int64
			copied, err = io.Copy(io.Discard, f)
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
			if err == nil && copied != int64(len(buf)) {
				err = errors.New("peer stream length mismatch")
			}
		} else {
			err = e
		}
	}
	rate := 8 / time.Since(start).Seconds()
	stream := Benchmark{"large_upload_share_streaming", "MiB_per_second", rate, l.StreamMiBPerSecond, "greater_or_equal", err == nil && rate >= l.StreamMiBPerSecond}
	con := lat("concurrent_api_sandbox_access", l.ConcurrentP95MS, func(sample int) error {
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := range 8 {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				name := fmt.Sprintf("con-%02d-%d", sample, i)
				if e := durableFilePublication(a, name, []byte("api")); e != nil {
					errs <- e
					return
				}
				value, e := os.ReadFile(filepath.Join(b, name))
				if e != nil {
					errs <- e
				} else if string(value) != "api" {
					errs <- errors.New("concurrent peer read mismatch")
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			return e
		}
		return nil
	})
	var st os.FileInfo
	_ = st // capacity is recorded as a criterion via platform helper
	return []Benchmark{meta, small, stream, con, capacityBenchmark(a, l.MinimumFreeBytes)}
}

func durableFilePublication(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".publish-")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err = temp.Chmod(0o600); err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tempName, filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func durableDirectoryPublication(parent, name string, files int) error {
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(parent, ".revision-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	for i := range files {
		if err := durableFilePublication(temp, fmt.Sprintf("%02d", i), []byte("skill")); err != nil {
			return err
		}
	}
	if err := syncDirectory(temp); err != nil {
		return err
	}
	if err := os.Rename(temp, filepath.Join(parent, name)); err != nil {
		return err
	}
	return syncDirectory(parent)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	err = dir.Sync()
	return errors.Join(err, dir.Close())
}
