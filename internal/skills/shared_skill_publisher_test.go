package skills

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/fsops"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

type testSharedSkillHomes struct {
	filesystem sandbox.Filesystem
	keys       []home.Key
}

func (h *testSharedSkillHomes) UseSharedSkillFilesystem(_ context.Context, key home.Key, use func(sandbox.Filesystem) error) error {
	h.keys = append(h.keys, key)
	return use(h.filesystem)
}

func testSharedSkillPublisher(t *testing.T) (*SharedSkillPublisher, *testSharedSkillHomes, sandbox.Filesystem) {
	t.Helper()
	filesystem, err := fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = filesystem.Close() })
	homes := &testSharedSkillHomes{filesystem: filesystem}
	publisher, err := NewSharedSkillPublisher(homes)
	if err != nil {
		t.Fatal(err)
	}
	return publisher, homes, filesystem
}

func validSharedSkillRequest() SharedSkillPublishRequest {
	return SharedSkillPublishRequest{
		Root: home.SystemSkills(), Name: "example",
		Metadata: SharedSkillMetadata{Status: SkillStatusActive, Metadata: map[string]any{"created_by": "test", "nested": map[string]any{"number": json.Number("1")}}, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC), LegacyLifecycleVersion: 1},
		Files:    []SharedSkillFile{{Path: MainFile, Content: []byte("---\nname: example\ndescription: example\n---\n"), Mode: 0o644}, {Path: "bin/run", Content: []byte{0, 0xff, 'x'}, Mode: 0o755}},
	}
}

func TestSharedSkillPublisherCreatesUpdatesAndPreservesBinaryModes(t *testing.T) {
	publisher, homes, filesystem := testSharedSkillPublisher(t)
	request := validSharedSkillRequest()
	first, err := publisher.Publish(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(homes.keys) != 1 || homes.keys[0] != home.SystemSkills() {
		t.Fatalf("Home keys = %#v, want only SystemSkills", homes.keys)
	}
	entry := path.Join(sandbox.PathWorkspace, ".stella-revisions", request.Name, first, "bin/run")
	reader, info, err := filesystem.Read(context.Background(), entry, sandbox.ReadOptions{MaxBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(reader)
	if closeErr := reader.Close(); readErr != nil || closeErr != nil || string(body) != string([]byte{0, 0xff, 'x'}) || info.Mode.Perm() != 0o755 {
		t.Fatalf("binary file = %x, mode=%#o, errors=%v/%v", body, info.Mode.Perm(), readErr, closeErr)
	}

	request.ExpectedDigest = first
	request.Files[0].Content = append(request.Files[0].Content, '#')
	request.Metadata.UpdatedAt = request.Metadata.UpdatedAt.Add(time.Second)
	second, err := publisher.Publish(context.Background(), request)
	if err != nil || second == first {
		t.Fatalf("update = %q, %v; want changed digest", second, err)
	}
	// Re-publishing the exact selected revision with that digest is deliberately
	// idempotent: both a previous selection and this publication are valid.
	request.ExpectedDigest = second
	if got, err := publisher.Publish(context.Background(), request); err != nil || got != second {
		t.Fatalf("idempotent publication = %q, %v", got, err)
	}
}

func TestSharedSkillPublisherRejectsInvalidRequestsBeforeHomeAccess(t *testing.T) {
	publisher, homes, _ := testSharedSkillPublisher(t)
	cases := map[string]func(*SharedSkillPublishRequest){
		"name":            func(r *SharedSkillPublishRequest) { r.Name = "bad/name" },
		"expected digest": func(r *SharedSkillPublishRequest) { r.ExpectedDigest = "ABC" },
		"reserved path":   func(r *SharedSkillPublishRequest) { r.Files[1].Path = skillMetadataFile },
		"bad mode":        func(r *SharedSkillPublishRequest) { r.Files[1].Mode = fs.ModeSymlink | 0o777 },
		"empty main":      func(r *SharedSkillPublishRequest) { r.Files[0].Content = nil },
		"missing main":    func(r *SharedSkillPublishRequest) { r.Files[0].Path = "README.md" },
		"no created by":   func(r *SharedSkillPublishRequest) { r.Metadata.Metadata = map[string]any{} },
		"bad lifecycle":   func(r *SharedSkillPublishRequest) { r.Metadata.LegacyLifecycleVersion = 0 },
		"non UTC timestamp": func(r *SharedSkillPublishRequest) {
			r.Metadata.CreatedAt = r.Metadata.CreatedAt.In(time.FixedZone("offset", 3600))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			request := validSharedSkillRequest()
			change(&request)
			if _, err := publisher.Publish(context.Background(), request); err == nil {
				t.Fatal("invalid request succeeded")
			}
		})
	}
	if len(homes.keys) != 0 {
		t.Fatalf("invalid requests opened Home: %#v", homes.keys)
	}
}

func TestSharedSkillPublisherConflictsAndSerializesCreate(t *testing.T) {
	publisher, _, filesystem := testSharedSkillPublisher(t)
	request := validSharedSkillRequest()
	if err := filesystem.Mkdir(context.Background(), path.Join(sandbox.PathWorkspace, request.Name), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Publish(context.Background(), request); !errors.Is(err, ErrSharedSkillConflict) {
		t.Fatalf("ordinary occupant error = %v", err)
	}
	if err := filesystem.Remove(context.Background(), path.Join(sandbox.PathWorkspace, request.Name), true); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Go(func() {
			<-start
			_, err := publisher.Publish(context.Background(), validSharedSkillRequest())
			results <- err
		})
	}
	close(start)
	wait.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrSharedSkillConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent create successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}
	stale := validSharedSkillRequest()
	stale.ExpectedDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := publisher.Publish(context.Background(), stale); !errors.Is(err, ErrSharedSkillConflict) {
		t.Fatalf("stale expected digest error = %v", err)
	}
}

func TestSharedSkillPublisherSnapshotsCallerContent(t *testing.T) {
	publisher, _, filesystem := testSharedSkillPublisher(t)
	request := validSharedSkillRequest()
	original := append([]byte(nil), request.Files[1].Content...)
	// The access wrapper mutates the caller after the digest and publication body
	// have been snapshotted but before the managed publisher reads either.
	publisher.homes = sharedSkillHomesFunc(func(_ context.Context, key home.Key, use func(sandbox.Filesystem) error) error {
		request.Files[1].Content[1] = 0
		request.Metadata.Metadata["created_by"] = "changed"
		return use(filesystem)
	})
	digest, err := publisher.Publish(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	reader, _, err := filesystem.Read(context.Background(), path.Join(sandbox.PathWorkspace, ".stella-revisions", request.Name, digest, "bin/run"), sandbox.ReadOptions{MaxBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read published snapshot: read=%v close=%v", readErr, closeErr)
	}
	if string(got) != string(original) {
		t.Fatalf("published bytes = %x, want snapshot %x", got, original)
	}
}

func TestSharedSkillPublisherKeepsSystemAgentRootsIsolated(t *testing.T) {
	firstPublisher, _, firstFilesystem := testSharedSkillPublisher(t)
	secondFilesystem, err := fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondFilesystem.Close() })
	agentRoot := home.SystemAgentSkills("agent-a")
	firstPublisher.homes = sharedSkillHomesFunc(func(_ context.Context, key home.Key, use func(sandbox.Filesystem) error) error {
		switch key {
		case home.SystemSkills():
			return use(firstFilesystem)
		case agentRoot:
			return use(secondFilesystem)
		default:
			return errors.New("unexpected Home key")
		}
	})
	request := validSharedSkillRequest()
	if _, err := firstPublisher.Publish(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.Root = agentRoot
	if _, err := firstPublisher.Publish(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	for _, filesystem := range []sandbox.Filesystem{firstFilesystem, secondFilesystem} {
		target, err := filesystem.(sandbox.ManagedSkillTargetInspector).InspectManagedSkillTarget(context.Background(), path.Join(sandbox.PathWorkspace, request.Name))
		if err != nil || !target.Managed {
			t.Fatalf("isolated target = %+v, %v", target, err)
		}
	}
}

func TestSharedSkillPublisherObservesOnlyVerifiedSuccess(t *testing.T) {
	filesystem, err := fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = filesystem.Close() })
	telemetry, err := NewRevisionTelemetry(RevisionTelemetryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	root, err := SystemFilesystemCatalogRoot(sandbox.PathWorkspace, sandbox.HomeAttachment{HomeID: "opaque-home", StoreID: "store", Locator: "private"})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := NewSharedSkillPublisherWithRevisionTelemetry(&testSharedSkillHomes{filesystem: filesystem}, telemetry, func(home.Key) (FilesystemCatalogRoot, error) { return root, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Publish(context.Background(), validSharedSkillRequest()); err != nil {
		t.Fatal(err)
	}
	key := revisionKey(root)
	telemetry.mu.RLock()
	observation := telemetry.observed[key]
	telemetry.mu.RUnlock()
	if observation.count != 1 || observation.bytes == 0 {
		t.Fatalf("success observation = %+v", observation)
	}
	if _, err := publisher.Publish(context.Background(), validSharedSkillRequest()); !errors.Is(err, ErrSharedSkillConflict) {
		t.Fatalf("conflict = %v", err)
	}
	telemetry.mu.RLock()
	afterConflict := telemetry.observed[key]
	telemetry.mu.RUnlock()
	if afterConflict != observation {
		t.Fatalf("conflict changed telemetry: before=%+v after=%+v", observation, afterConflict)
	}
}

func TestSharedSkillPublisherTelemetryFailuresKeepVerifiedSuccess(t *testing.T) {
	base, err := fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	root, err := SystemFilesystemCatalogRoot(sandbox.PathWorkspace, sandbox.HomeAttachment{HomeID: "opaque-home", StoreID: "store", Locator: "private"})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("resolver", func(t *testing.T) {
		logs := &revisionLogHandler{}
		previous := slog.Default()
		slog.SetDefault(slog.New(logs))
		t.Cleanup(func() { slog.SetDefault(previous) })
		telemetry, err := NewRevisionTelemetry(RevisionTelemetryConfig{})
		if err != nil {
			t.Fatal(err)
		}
		publisher, err := NewSharedSkillPublisherWithRevisionTelemetry(&testSharedSkillHomes{filesystem: base}, telemetry, func(home.Key) (FilesystemCatalogRoot, error) {
			return FilesystemCatalogRoot{}, errors.New("/workspace/secret")
		})
		if err != nil {
			t.Fatal(err)
		}
		if digest, err := publisher.Publish(context.Background(), validSharedSkillRequest()); err != nil || digest == "" {
			t.Fatalf("publish = %q, %v", digest, err)
		}
		if record := logs.last("shared Skill revision telemetry failed after publication"); record.attrs["reason"] != "root_unavailable" {
			t.Fatalf("resolver log = %+v", record)
		}
	})
	t.Run("scan", func(t *testing.T) {
		logs := &revisionLogHandler{}
		telemetry, err := NewRevisionTelemetry(RevisionTelemetryConfig{Logger: slog.New(logs)})
		if err != nil {
			t.Fatal(err)
		}
		failing := &telemetryFailFilesystem{Filesystem: base}
		publisher, err := NewSharedSkillPublisherWithRevisionTelemetry(&testSharedSkillHomes{filesystem: failing}, telemetry, func(home.Key) (FilesystemCatalogRoot, error) { return root, nil })
		if err != nil {
			t.Fatal(err)
		}
		request := validSharedSkillRequest()
		request.Name = "telemetry-failure"
		if digest, err := publisher.Publish(context.Background(), request); err != nil || digest == "" {
			t.Fatalf("publish = %q, %v", digest, err)
		}
		record := logs.last("skill revision telemetry collection failed")
		if record.attrs["reason"] != "scan_failed" {
			t.Fatalf("scan log = %+v", record)
		}
		for _, value := range record.attrs {
			if value == sandbox.PathWorkspace || value == "/workspace/secret" {
				t.Fatalf("canonical path leaked: %+v", record)
			}
		}
	})
}

func TestSharedSkillPublisherTelemetrySkipsPrevalidationAndUnknownOutcome(t *testing.T) {
	publisher, _, filesystem := testSharedSkillPublisher(t)
	telemetry, err := NewRevisionTelemetry(RevisionTelemetryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	root, err := SystemFilesystemCatalogRoot(sandbox.PathWorkspace, sandbox.HomeAttachment{HomeID: "opaque-home", StoreID: "store", Locator: "private"})
	if err != nil {
		t.Fatal(err)
	}
	publisher.revisionTelemetry = telemetry
	publisher.catalogRoot = func(home.Key) (FilesystemCatalogRoot, error) { return root, nil }
	bad := validSharedSkillRequest()
	bad.Name = "bad/name"
	if _, err := publisher.Publish(context.Background(), bad); err == nil {
		t.Fatal("invalid request succeeded")
	}
	unknown := &unknownPublishFilesystem{Filesystem: filesystem}
	publisher.homes = sharedSkillHomesFunc(func(_ context.Context, _ home.Key, use func(sandbox.Filesystem) error) error { return use(unknown) })
	if _, err := publisher.Publish(context.Background(), validSharedSkillRequest()); !errors.Is(err, sandbox.ErrOutcomeUnknown) {
		t.Fatal("unknown outcome was not preserved")
	}
	telemetry.mu.RLock()
	_, observed := telemetry.observed[revisionKey(root)]
	telemetry.mu.RUnlock()
	if observed {
		t.Fatal("prevalidation or outcome-unknown observed telemetry")
	}
}

type telemetryFailFilesystem struct{ sandbox.Filesystem }

func (f *telemetryFailFilesystem) InspectManagedSkillTarget(ctx context.Context, name string) (sandbox.ManagedSkillTarget, error) {
	return f.Filesystem.(sandbox.ManagedSkillTargetInspector).InspectManagedSkillTarget(ctx, name)
}

func (f *telemetryFailFilesystem) PublishManagedSkill(ctx context.Context, root, name, digest string, publication sandbox.ManagedSkillPublication) error {
	return f.Filesystem.(sandbox.ManagedSkillPublisher).PublishManagedSkill(ctx, root, name, digest, publication)
}

func (f *telemetryFailFilesystem) List(context.Context, string) ([]sandbox.DirEntry, error) {
	return nil, errors.New("/workspace/secret")
}

type sharedSkillHomesFunc func(context.Context, home.Key, func(sandbox.Filesystem) error) error

func (f sharedSkillHomesFunc) UseSharedSkillFilesystem(ctx context.Context, key home.Key, use func(sandbox.Filesystem) error) error {
	return f(ctx, key, use)
}

type unknownPublishFilesystem struct {
	sandbox.Filesystem
	publishes int
}

type genericErrorPublishFilesystem struct {
	sandbox.Filesystem
	err       error
	publishes int
}

func (f *genericErrorPublishFilesystem) InspectManagedSkillTarget(ctx context.Context, name string) (sandbox.ManagedSkillTarget, error) {
	return f.Filesystem.(sandbox.ManagedSkillTargetInspector).InspectManagedSkillTarget(ctx, name)
}

func (f *genericErrorPublishFilesystem) PublishManagedSkill(context.Context, string, string, string, sandbox.ManagedSkillPublication) error {
	f.publishes++
	return f.err
}

type mismatchedSelectionFilesystem struct {
	sandbox.Filesystem
	inspections int
	publishes   int
}

func (f *mismatchedSelectionFilesystem) InspectManagedSkillTarget(ctx context.Context, name string) (sandbox.ManagedSkillTarget, error) {
	f.inspections++
	if f.inspections == 1 {
		return f.Filesystem.(sandbox.ManagedSkillTargetInspector).InspectManagedSkillTarget(ctx, name)
	}
	return sandbox.ManagedSkillTarget{Managed: true, Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
}

func (f *mismatchedSelectionFilesystem) PublishManagedSkill(context.Context, string, string, string, sandbox.ManagedSkillPublication) error {
	f.publishes++
	return nil
}

func (f *unknownPublishFilesystem) InspectManagedSkillTarget(ctx context.Context, name string) (sandbox.ManagedSkillTarget, error) {
	return f.Filesystem.(sandbox.ManagedSkillTargetInspector).InspectManagedSkillTarget(ctx, name)
}

func (f *unknownPublishFilesystem) PublishManagedSkill(context.Context, string, string, string, sandbox.ManagedSkillPublication) error {
	f.publishes++
	return sandbox.ErrOutcomeUnknown
}

func TestSharedSkillPublisherFailsClosedForCapabilitiesAndUnknownOutcome(t *testing.T) {
	if _, err := NewSharedSkillPublisher(nil); err == nil {
		t.Fatal("nil Home access succeeded")
	}
	publisher, _, filesystem := testSharedSkillPublisher(t)
	publisher.homes = sharedSkillHomesFunc(func(_ context.Context, _ home.Key, use func(sandbox.Filesystem) error) error {
		return use(struct{ sandbox.Filesystem }{filesystem}) // masks both optional capabilities
	})
	if _, err := publisher.Publish(context.Background(), validSharedSkillRequest()); err == nil {
		t.Fatal("missing managed capabilities succeeded")
	}
	unknown := &unknownPublishFilesystem{Filesystem: filesystem}
	publisher.homes = sharedSkillHomesFunc(func(_ context.Context, _ home.Key, use func(sandbox.Filesystem) error) error { return use(unknown) })
	if _, err := publisher.Publish(context.Background(), validSharedSkillRequest()); !errors.Is(err, sandbox.ErrOutcomeUnknown) || unknown.publishes != 1 {
		t.Fatalf("outcome unknown = %v, publishes=%d; want preserved once", err, unknown.publishes)
	}
}

func TestSharedSkillPublisherTreatsPostPublishFailuresAsOutcomeUnknown(t *testing.T) {
	publisher, _, filesystem := testSharedSkillPublisher(t)
	generic := errors.New("publisher disconnected")
	failing := &genericErrorPublishFilesystem{Filesystem: filesystem, err: generic}
	publisher.homes = sharedSkillHomesFunc(func(_ context.Context, _ home.Key, use func(sandbox.Filesystem) error) error { return use(failing) })
	if _, err := publisher.Publish(context.Background(), validSharedSkillRequest()); !errors.Is(err, sandbox.ErrOutcomeUnknown) || !errors.Is(err, generic) || failing.publishes != 1 {
		t.Fatalf("generic publisher error = %v, publishes=%d; want unknown + underlying error once", err, failing.publishes)
	}

	mismatch := &mismatchedSelectionFilesystem{Filesystem: filesystem}
	publisher.homes = sharedSkillHomesFunc(func(_ context.Context, _ home.Key, use func(sandbox.Filesystem) error) error { return use(mismatch) })
	if _, err := publisher.Publish(context.Background(), validSharedSkillRequest()); !errors.Is(err, sandbox.ErrOutcomeUnknown) || errors.Is(err, ErrSharedSkillConflict) || mismatch.publishes != 1 || mismatch.inspections != 2 {
		t.Fatalf("selection mismatch = %v, publishes=%d inspections=%d; want unknown once and no clean conflict", err, mismatch.publishes, mismatch.inspections)
	}
}
