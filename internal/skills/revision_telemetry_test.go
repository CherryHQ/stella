package skills

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/CherryHQ/stella/internal/fsops"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

const revisionDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestRevisionTelemetryMetricsAreScopeOnly(t *testing.T) {
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	telemetry, err := NewRevisionTelemetry(RevisionTelemetryConfig{MeterProvider: provider, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	userFS, userRoot, userDir := revisionTestRoot(t, "user-home", "user")
	systemFS, systemRoot, systemDir := revisionTestRoot(t, "system-home", "system")
	writeRevisionTree(t, userDir, "first", revisionDigest, map[string]string{"SKILL.md": "123"}, now.Add(-2*time.Hour))
	writeRevisionTree(t, systemDir, "second", revisionDigest, map[string]string{"SKILL.md": "4567"}, now.Add(-time.Hour))
	for _, item := range []struct {
		fs   sandbox.Filesystem
		root FilesystemCatalogRoot
	}{{userFS, userRoot}, {systemFS, systemRoot}} {
		if err := telemetry.Observe(context.Background(), item.fs, item.root); err != nil {
			t.Fatal(err)
		}
	}
	var result metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &result); err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]float64{}
	for _, scope := range result.ScopeMetrics {
		for _, item := range scope.Metrics {
			switch data := item.Data.(type) {
			case metricdata.Gauge[int64]:
				for _, point := range data.DataPoints {
					got[item.Name] = addMetricPoint(t, got[item.Name], point.Attributes, float64(point.Value))
				}
			case metricdata.Gauge[float64]:
				for _, point := range data.DataPoints {
					got[item.Name] = addMetricPoint(t, got[item.Name], point.Attributes, point.Value)
				}
			}
		}
	}
	for _, name := range []string{"stella.skill.revisions.count", "stella.skill.revisions.bytes", "stella.skill.revisions.oldest_age"} {
		if len(got[name]) != 2 {
			t.Fatalf("%s scopes = %#v", name, got[name])
		}
	}
	if got["stella.skill.revisions.count"]["user"] != 1 || got["stella.skill.revisions.bytes"]["system"] != 4 || got["stella.skill.revisions.oldest_age"]["user"] != 7200 {
		t.Fatalf("metrics = %#v", got)
	}
}

func TestRevisionTelemetryWarningDedupRecoveryAndPathSafeFailure(t *testing.T) {
	logs := &revisionLogHandler{}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	telemetry, err := NewRevisionTelemetry(RevisionTelemetryConfig{Logger: slog.New(logs), Now: func() time.Time { return now }, Thresholds: RevisionThresholds{Count: 1}})
	if err != nil {
		t.Fatal(err)
	}
	filesystem, root, directory := revisionTestRoot(t, "opaque-home", "user")
	writeRevisionTree(t, directory, "skill", revisionDigest, map[string]string{"SKILL.md": "x"}, now)
	if err := telemetry.Observe(context.Background(), filesystem, root); err != nil {
		t.Fatal(err)
	}
	if err := telemetry.Observe(context.Background(), filesystem, root); err != nil {
		t.Fatal(err)
	}
	writeRevisionTree(t, directory, "other", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", map[string]string{"SKILL.md": "x"}, now)
	if err := telemetry.Observe(context.Background(), filesystem, root); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Remove(context.Background(), sandbox.PathWorkspace+"/.stella-revisions", true); err != nil {
		t.Fatal(err)
	}
	if err := telemetry.Observe(context.Background(), filesystem, root); err != nil {
		t.Fatal(err)
	}
	writeRevisionTree(t, directory, "again", revisionDigest, map[string]string{"SKILL.md": "x"}, now)
	if err := telemetry.Observe(context.Background(), filesystem, root); err != nil {
		t.Fatal(err)
	}
	if got := logs.count("skill retained revision capacity exceeded"); got != 3 {
		t.Fatalf("capacity warnings = %d, want 3", got)
	}
	if warning := logs.last("skill retained revision capacity exceeded").attrs; warning["root_id"] == "" || warning["root_id"] == "opaque-home" || warning["scope"] != "user" || warning["home_id"] != "" {
		t.Fatalf("capacity warning identity = %#v", warning)
	}
	for _, record := range logs.records {
		for key, value := range record.attrs {
			if key != "root_id" && (value == "opaque-home" || value == sandbox.PathWorkspace || value == revisionDigest) {
				t.Fatalf("unsafe log attr %s=%q", key, value)
			}
		}
	}
	var calls int
	failing := &revisionFilesystem{Filesystem: filesystem, list: func(context.Context, string) ([]sandbox.DirEntry, error) {
		calls++
		return nil, errors.New("secret /workspace/path")
	}}
	if err := telemetry.Observe(context.Background(), failing, root); err == nil {
		t.Fatal("failing scan succeeded")
	}
	if calls != 1 {
		t.Fatalf("failed scan retried %d times", calls)
	}
	if logs.last("skill revision telemetry collection failed").attrs["reason"] != "scan_failed" {
		t.Fatal("failure reason not sanitized")
	}
	telemetry.mu.RLock()
	snapshot := telemetry.observed[revisionKey(root)]
	telemetry.mu.RUnlock()
	if snapshot.count != 1 {
		t.Fatalf("failed scan replaced snapshot: %+v", snapshot)
	}
}

func TestRevisionTelemetryMissingDirectoryUpdatesToZero(t *testing.T) {
	telemetry, err := NewRevisionTelemetry(RevisionTelemetryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	filesystem, root, directory := revisionTestRoot(t, "home", "user")
	writeRevisionTree(t, directory, "skill", revisionDigest, map[string]string{"SKILL.md": "x"}, time.Now().UTC())
	if err := telemetry.Observe(context.Background(), filesystem, root); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Remove(context.Background(), sandbox.PathWorkspace+"/.stella-revisions", true); err != nil {
		t.Fatal(err)
	}
	if err := telemetry.Observe(context.Background(), filesystem, root); err != nil {
		t.Fatal(err)
	}
	telemetry.mu.RLock()
	snapshot := telemetry.observed[revisionKey(root)]
	telemetry.mu.RUnlock()
	if snapshot.count != 0 || snapshot.bytes != 0 || snapshot.oldestAge != 0 {
		t.Fatalf("missing directory snapshot = %+v", snapshot)
	}
}

func TestScanRetainedRevisionsRejectsAllBoundaries(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(t *testing.T, directory string) sandbox.Filesystem
		limits   RevisionScanLimits
		canceled bool
	}{
		{"name entry", func(_ *testing.T, _ string) sandbox.Filesystem { return nil }, RevisionScanLimits{MaxDepth: 1, MaxEntries: 0, MaxBytes: 10}, false},
		{"digest entry", func(_ *testing.T, _ string) sandbox.Filesystem { return nil }, RevisionScanLimits{MaxDepth: 1, MaxEntries: 1, MaxBytes: 10}, false},
		{"bytes", func(_ *testing.T, _ string) sandbox.Filesystem { return nil }, RevisionScanLimits{MaxDepth: 1, MaxEntries: 10, MaxBytes: 0}, false},
		{"depth", func(t *testing.T, directory string) sandbox.Filesystem {
			if err := os.MkdirAll(filepath.Join(directory, ".stella-revisions", "skill", revisionDigest, "a", "b"), 0o755); err != nil {
				t.Fatal(err)
			}
			return nil
		}, RevisionScanLimits{MaxDepth: 0, MaxEntries: 10, MaxBytes: 10}, false},
		{"malformed digest", func(t *testing.T, directory string) sandbox.Filesystem {
			if err := os.MkdirAll(filepath.Join(directory, ".stella-revisions", "skill", "BAD"), 0o755); err != nil {
				t.Fatal(err)
			}
			return nil
		}, RevisionScanLimits{MaxDepth: 2, MaxEntries: 10, MaxBytes: 10}, false},
		{"malformed name", func(t *testing.T, directory string) sandbox.Filesystem {
			if err := os.MkdirAll(filepath.Join(directory, ".stella-revisions", "Bad", revisionDigest), 0o755); err != nil {
				t.Fatal(err)
			}
			return nil
		}, RevisionScanLimits{MaxDepth: 2, MaxEntries: 10, MaxBytes: 10}, false},
		{"canceled", func(_ *testing.T, _ string) sandbox.Filesystem { return nil }, RevisionScanLimits{MaxDepth: 2, MaxEntries: 10, MaxBytes: 10}, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			filesystem, root, directory := revisionTestRoot(t, "home", "user")
			writeRevisionTree(t, directory, "skill", revisionDigest, map[string]string{"SKILL.md": "x"}, time.Now().UTC())
			if replacement := test.setup(t, directory); replacement != nil {
				filesystem = replacement
			}
			ctx := context.Background()
			if test.canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := scanRetainedRevisions(ctx, filesystem, root, test.limits, time.Now().UTC()); err == nil {
				t.Fatal("scan succeeded")
			}
		})
	}
}

func TestScanRetainedRevisionsRejectsSymlinkSpecialAndChangingMetadata(t *testing.T) {
	for _, test := range []struct {
		name string
		wrap func(sandbox.Filesystem, string) sandbox.Filesystem
	}{
		{"special", func(filesystem sandbox.Filesystem, target string) sandbox.Filesystem {
			return &revisionFilesystem{Filesystem: filesystem, stat: func(ctx context.Context, name string) (sandbox.FileInfo, error) {
				info, err := filesystem.Stat(ctx, name)
				if name == target {
					info.Mode = fs.ModeNamedPipe
					info.IsDir = false
				}
				return info, err
			}}
		}},
		{"changed metadata", func(filesystem sandbox.Filesystem, target string) sandbox.Filesystem {
			return &revisionFilesystem{Filesystem: filesystem, list: func(ctx context.Context, name string) ([]sandbox.DirEntry, error) {
				entries, err := filesystem.List(ctx, name)
				if name == path.Dir(target) && len(entries) > 0 {
					entries[0].Size++
				}
				return entries, err
			}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			filesystem, root, directory := revisionTestRoot(t, "home", "user")
			writeRevisionTree(t, directory, "skill", revisionDigest, map[string]string{"SKILL.md": "x"}, time.Now().UTC())
			target := sandbox.PathWorkspace + "/.stella-revisions/skill/" + revisionDigest + "/SKILL.md"
			if _, err := scanRetainedRevisions(context.Background(), test.wrap(filesystem, target), root, RevisionScanLimits{MaxDepth: 2, MaxEntries: 10, MaxBytes: 10}, time.Now().UTC()); err == nil {
				t.Fatal("scan succeeded")
			}
		})
	}
	// A real symlink must be rejected from List metadata before it can be
	// followed by Stat. It is skipped only on hosts that cannot create one.
	filesystem, root, directory := revisionTestRoot(t, "home", "user")
	base := filepath.Join(directory, ".stella-revisions", "skill", revisionDigest)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", filepath.Join(base, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := scanRetainedRevisions(context.Background(), filesystem, root, RevisionScanLimits{MaxDepth: 2, MaxEntries: 10, MaxBytes: 10}, time.Now().UTC()); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestRevisionTelemetryConcurrentObserveForgetAndCollect(t *testing.T) {
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	telemetry, err := NewRevisionTelemetry(RevisionTelemetryConfig{MeterProvider: provider})
	if err != nil {
		t.Fatal(err)
	}
	filesystem, root, directory := revisionTestRoot(t, "home", "user")
	writeRevisionTree(t, directory, "skill", revisionDigest, map[string]string{"SKILL.md": "x"}, time.Now().UTC())
	start := make(chan struct{})
	var group sync.WaitGroup
	for range 3 {
		group.Go(func() {
			<-start
			for range 100 {
				_ = telemetry.Observe(context.Background(), filesystem, root)
				telemetry.Forget(root)
			}
		})
	}
	group.Go(func() {
		<-start
		for range 100 {
			var result metricdata.ResourceMetrics
			if err := reader.Collect(context.Background(), &result); err != nil {
				t.Error(err)
			}
		}
	})
	close(start)
	group.Wait()
}

func TestRevisionTelemetryForgetDiscardsInFlightObservation(t *testing.T) {
	logs := &revisionLogHandler{}
	telemetry, err := NewRevisionTelemetry(RevisionTelemetryConfig{Logger: slog.New(logs), Thresholds: RevisionThresholds{Count: 1}})
	if err != nil {
		t.Fatal(err)
	}
	filesystem, root, directory := revisionTestRoot(t, "home", "user")
	writeRevisionTree(t, directory, "skill", revisionDigest, map[string]string{"SKILL.md": "x"}, time.Now().UTC())
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls int
	var callsMu sync.Mutex
	blocked := &revisionFilesystem{Filesystem: filesystem, list: func(ctx context.Context, name string) ([]sandbox.DirEntry, error) {
		callsMu.Lock()
		first := calls == 0
		calls++
		callsMu.Unlock()
		if first {
			close(entered)
			<-release
		}
		return filesystem.List(ctx, name)
	}}
	finished := make(chan error, 1)
	go func() { finished <- telemetry.Observe(context.Background(), blocked, root) }()
	<-entered
	telemetry.Forget(root)
	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("discarded observation = %v", err)
	}
	key := revisionKey(root)
	telemetry.mu.RLock()
	_, observed := telemetry.observed[key]
	_, stateExists := telemetry.roots[key]
	telemetry.mu.RUnlock()
	if observed || stateExists {
		t.Fatalf("forgotten root remains cached: observed=%t state=%t", observed, stateExists)
	}
	if logs.count("skill retained revision capacity exceeded") != 0 {
		t.Fatal("discarded observation emitted a capacity warning")
	}
	if err := telemetry.Observe(context.Background(), blocked, root); err != nil {
		t.Fatal(err)
	}
	telemetry.mu.RLock()
	_, observed = telemetry.observed[key]
	telemetry.mu.RUnlock()
	if !observed {
		t.Fatal("fresh observation after Forget did not commit")
	}
}

func TestRevisionTelemetryNewestObserveWinsAndFailureKeepsSnapshot(t *testing.T) {
	telemetry, err := NewRevisionTelemetry(RevisionTelemetryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	filesystem, root, directory := revisionTestRoot(t, "home", "user")
	writeRevisionTree(t, directory, "skill", revisionDigest, map[string]string{"SKILL.md": "old"}, time.Now().UTC())
	target := sandbox.PathWorkspace + "/.stella-revisions/skill/" + revisionDigest + "/SKILL.md"
	oldInfo, err := filesystem.Stat(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	older := &revisionFilesystem{Filesystem: filesystem, stat: func(ctx context.Context, name string) (sandbox.FileInfo, error) {
		if name == target {
			close(entered)
			<-release
			return oldInfo, nil
		}
		return filesystem.Stat(ctx, name)
	}}
	olderDone := make(chan error, 1)
	go func() { olderDone <- telemetry.Observe(context.Background(), older, root) }()
	<-entered
	if err := os.WriteFile(filepath.Join(directory, ".stella-revisions", "skill", revisionDigest, "SKILL.md"), []byte("newer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := telemetry.Observe(context.Background(), filesystem, root); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-olderDone; err != nil {
		t.Fatal(err)
	}
	key := revisionKey(root)
	telemetry.mu.RLock()
	snapshot := telemetry.observed[key]
	telemetry.mu.RUnlock()
	if snapshot.bytes != int64(len("newer")) {
		t.Fatalf("older scan overwrote newer snapshot: %+v", snapshot)
	}
	failing := &revisionFilesystem{Filesystem: filesystem, list: func(context.Context, string) ([]sandbox.DirEntry, error) { return nil, errors.New("scan failure") }}
	if err := telemetry.Observe(context.Background(), failing, root); err == nil {
		t.Fatal("latest failed scan succeeded")
	}
	telemetry.mu.RLock()
	snapshot = telemetry.observed[key]
	telemetry.mu.RUnlock()
	if snapshot.bytes != int64(len("newer")) {
		t.Fatalf("latest failure replaced prior snapshot: %+v", snapshot)
	}
}

type revisionFilesystem struct {
	sandbox.Filesystem
	list func(context.Context, string) ([]sandbox.DirEntry, error)
	stat func(context.Context, string) (sandbox.FileInfo, error)
}

func (f *revisionFilesystem) List(ctx context.Context, name string) ([]sandbox.DirEntry, error) {
	if f.list != nil {
		return f.list(ctx, name)
	}
	return f.Filesystem.List(ctx, name)
}

func (f *revisionFilesystem) Stat(ctx context.Context, name string) (sandbox.FileInfo, error) {
	if f.stat != nil {
		return f.stat(ctx, name)
	}
	return f.Filesystem.Stat(ctx, name)
}

type revisionLogRecord struct {
	message string
	attrs   map[string]string
}
type revisionLogHandler struct {
	mu      sync.Mutex
	records []revisionLogRecord
}

func (h *revisionLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *revisionLogHandler) Handle(_ context.Context, record slog.Record) error {
	values := map[string]string{}
	record.Attrs(func(a slog.Attr) bool { values[a.Key] = a.Value.String(); return true })
	h.mu.Lock()
	h.records = append(h.records, revisionLogRecord{record.Message, values})
	h.mu.Unlock()
	return nil
}
func (h *revisionLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *revisionLogHandler) WithGroup(string) slog.Handler      { return h }
func (h *revisionLogHandler) count(message string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.records {
		if r.message == message {
			n++
		}
	}
	return n
}

func (h *revisionLogHandler) last(message string) revisionLogRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.records) - 1; i >= 0; i-- {
		if h.records[i].message == message {
			return h.records[i]
		}
	}
	return revisionLogRecord{}
}

func addMetricPoint(t *testing.T, values map[string]float64, attrs attribute.Set, value float64) map[string]float64 {
	t.Helper()
	items := attrs.ToSlice()
	if len(items) != 1 || items[0].Key != "scope" {
		t.Fatalf("metric attributes = %v", attrs)
	}
	if values == nil {
		values = map[string]float64{}
	}
	values[items[0].Value.AsString()] = value
	return values
}

func revisionTestRoot(t *testing.T, homeID, scope string) (sandbox.Filesystem, FilesystemCatalogRoot, string) {
	t.Helper()
	directory := t.TempDir()
	filesystem, err := fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: directory}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = filesystem.Close() })
	attachment := sandbox.HomeAttachment{HomeID: homeID, StoreID: "store", Locator: "opaque"}
	var root FilesystemCatalogRoot
	if scope == "system" {
		root, err = SystemFilesystemCatalogRoot(sandbox.PathWorkspace, attachment)
	} else {
		root, err = UserFilesystemCatalogRoot(sandbox.PathWorkspace, attachment, "user-id")
	}
	if err != nil {
		t.Fatal(err)
	}
	return filesystem, root, directory
}

func writeRevisionTree(t *testing.T, directory, name, digest string, files map[string]string, modTime time.Time) {
	t.Helper()
	base := filepath.Join(directory, ".stella-revisions", name, digest)
	for filename, content := range files {
		full := filepath.Join(base, filename)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(base, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}
