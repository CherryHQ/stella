package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/fsops"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

type testHomeSkillHomes struct {
	mu          sync.Mutex
	filesystems map[*home.SkillRoot]sandbox.Filesystem
	used        []*home.SkillRoot
	err         error
}

func (h *testHomeSkillHomes) UseSkillFilesystem(_ context.Context, root *home.SkillRoot, use func(sandbox.Filesystem) error) error {
	h.mu.Lock()
	h.used = append(h.used, root)
	filesystem := h.filesystems[root]
	err := h.err
	h.mu.Unlock()
	if err != nil {
		return err
	}
	if filesystem == nil {
		return errors.New("unexpected typed Skill root")
	}
	return use(filesystem)
}

func testHomeSkillPublisher(t *testing.T, roots ...*home.SkillRoot) (*HomeSkillPublisher, *testHomeSkillHomes) {
	t.Helper()
	homes := &testHomeSkillHomes{filesystems: make(map[*home.SkillRoot]sandbox.Filesystem, len(roots))}
	for _, root := range roots {
		filesystem, err := fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: t.TempDir()}})
		if err != nil {
			t.Fatal(err)
		}
		homes.filesystems[root] = filesystem
		t.Cleanup(func() { _ = filesystem.Close() })
	}
	publisher, err := NewHomeSkillPublisher(homes)
	if err != nil {
		t.Fatal(err)
	}
	return publisher, homes
}

func validHomeSkillRequest(root *home.SkillRoot) HomeSkillPublishRequest {
	return HomeSkillPublishRequest{
		Root: root, Name: "example",
		Metadata: HomeSkillMetadata{Status: SkillStatusActive, Metadata: map[string]any{"created_by": "test"}, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC), LegacyLifecycleVersion: 1},
		Files: []HomeSkillFile{
			{Path: MainFile, Content: []byte("---\nname: example\ndescription: example\n---\n"), Mode: 0o644},
			{Path: "bin/run", Content: []byte{0, 0xff, 'x'}, Mode: 0o755},
		},
	}
}

func TestHomeSkillPublisherPublishesToAllTypedRootsWithoutCrossRootLeakage(t *testing.T) {
	system := home.SystemSkillCatalog()
	systemAgent, err := home.SystemAgentSkillCatalog("agent-a")
	if err != nil {
		t.Fatal(err)
	}
	user, err := home.UserSkillCatalog("user-a")
	if err != nil {
		t.Fatal(err)
	}
	userAgent, err := home.UserAgentSkillCatalog("user-a", "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	otherUser, err := home.UserSkillCatalog("user-b")
	if err != nil {
		t.Fatal(err)
	}
	roots := []*home.SkillRoot{system, systemAgent, user, userAgent, otherUser}
	publisher, homes := testHomeSkillPublisher(t, roots...)

	for _, root := range roots {
		request := validHomeSkillRequest(root)
		digest, err := publisher.Publish(context.Background(), request)
		if err != nil {
			t.Fatalf("publish root %p: %v", root, err)
		}
		filesystem := homes.filesystems[root]
		target, err := filesystem.(sandbox.ManagedSkillTargetInspector).InspectManagedSkillTarget(context.Background(), path.Join(sandbox.PathWorkspace, request.Name))
		if err != nil || !target.Managed || target.Digest != digest {
			t.Fatalf("root %p target = %+v, %v", root, target, err)
		}
	}
	homes.mu.Lock()
	defer homes.mu.Unlock()
	if len(homes.used) != len(roots) {
		t.Fatalf("typed Home opens = %d, want %d", len(homes.used), len(roots))
	}
	for i, root := range roots {
		if homes.used[i] != root {
			t.Fatalf("Home open %d used wrong root", i)
		}
	}
}

func TestHomeSkillPublisherCreateIdempotentAndConflictSemantics(t *testing.T) {
	root := home.SystemSkillCatalog()
	publisher, homes := testHomeSkillPublisher(t, root)
	request := validHomeSkillRequest(root)
	first, err := publisher.Publish(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedDigest = first
	if got, err := publisher.Publish(context.Background(), request); err != nil || got != first {
		t.Fatalf("idempotent publication = %q, %v", got, err)
	}
	request.ExpectedDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := publisher.Publish(context.Background(), request); !errors.Is(err, ErrHomeSkillConflict) {
		t.Fatalf("stale expected digest = %v", err)
	}

	other := validHomeSkillRequest(root)
	other.Name = "occupied"
	if err := homes.filesystems[root].Mkdir(context.Background(), path.Join(sandbox.PathWorkspace, other.Name), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Publish(context.Background(), other); !errors.Is(err, ErrHomeSkillConflict) {
		t.Fatalf("ordinary occupant = %v", err)
	}
}

func TestHomeSkillPublisherObservesOnlyVerifiedPublication(t *testing.T) {
	root := home.SystemSkillCatalog()
	_, homes := testHomeSkillPublisher(t, root)
	telemetry, err := NewRevisionTelemetry(RevisionTelemetryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	catalogRoot, err := SystemFilesystemCatalogRoot(sandbox.PathWorkspace, sandbox.HomeAttachment{HomeID: "opaque-home", StoreID: "store", Locator: "private"})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := NewHomeSkillPublisherWithRevisionTelemetry(homes, telemetry, func(got *home.SkillRoot) (FilesystemCatalogRoot, error) {
		if got != root {
			return FilesystemCatalogRoot{}, errors.New("wrong typed root")
		}
		return catalogRoot, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Publish(context.Background(), validHomeSkillRequest(root)); err != nil {
		t.Fatal(err)
	}
	key := revisionKey(catalogRoot)
	telemetry.mu.RLock()
	observation := telemetry.observed[key]
	telemetry.mu.RUnlock()
	if observation.count != 1 || observation.bytes == 0 {
		t.Fatalf("success observation = %+v", observation)
	}
	if _, err := publisher.Publish(context.Background(), validHomeSkillRequest(root)); !errors.Is(err, ErrHomeSkillConflict) {
		t.Fatalf("conflict = %v", err)
	}
	telemetry.mu.RLock()
	afterConflict := telemetry.observed[key]
	telemetry.mu.RUnlock()
	if afterConflict != observation {
		t.Fatalf("conflict changed telemetry: before=%+v after=%+v", observation, afterConflict)
	}
}

func TestHomeSkillPublisherValidatesAndSnapshotsBeforeHomeAccess(t *testing.T) {
	if _, err := NewHomeSkillPublisher(nil); err == nil {
		t.Fatal("nil Home filesystem access succeeded")
	}
	root := home.SystemSkillCatalog()
	publisher, homes := testHomeSkillPublisher(t, root)
	request := validHomeSkillRequest(nil)
	if _, err := publisher.Publish(context.Background(), request); err == nil {
		t.Fatal("nil root succeeded")
	}
	request = validHomeSkillRequest(root)
	request.Name = "bad/name"
	if _, err := publisher.Publish(context.Background(), request); err == nil {
		t.Fatal("invalid name succeeded")
	}
	homes.mu.Lock()
	if len(homes.used) != 0 {
		t.Fatalf("invalid request opened Home %d times", len(homes.used))
	}
	homes.mu.Unlock()

	original := append([]byte(nil), validHomeSkillRequest(root).Files[1].Content...)
	request = validHomeSkillRequest(root)
	publisher.homes = homeSkillFilesystemAccessFunc(func(_ context.Context, got *home.SkillRoot, use func(sandbox.Filesystem) error) error {
		if got != root {
			return errors.New("wrong root")
		}
		request.Files[1].Content[1] = 0
		request.Metadata.Metadata["created_by"] = "changed"
		return use(homes.filesystems[root])
	})
	digest, err := publisher.Publish(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	reader, _, err := homes.filesystems[root].Read(context.Background(), path.Join(sandbox.PathWorkspace, ".stella-revisions", request.Name, digest, "bin/run"), sandbox.ReadOptions{MaxBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || string(got) != string(original) {
		t.Fatalf("published snapshot = %x, errors=%v/%v", got, readErr, closeErr)
	}
}

func TestHomeSkillPublisherPrevalidatesPublicationBounds(t *testing.T) {
	root := home.SystemSkillCatalog()
	publisher, homes := testHomeSkillPublisher(t, root)
	cases := []struct {
		name   string
		mutate func(*HomeSkillPublishRequest)
	}{
		{
			name: "entry count includes metadata",
			mutate: func(request *HomeSkillPublishRequest) {
				request.Files = request.Files[:1]
				for i := range maxManagedTreeEntries - 1 {
					request.Files = append(request.Files, HomeSkillFile{Path: fmt.Sprintf("file-%d", i), Content: []byte("x"), Mode: 0o644})
				}
			},
		},
		{
			name: "caller file size",
			mutate: func(request *HomeSkillPublishRequest) {
				request.Files[1].Content = make([]byte, maxManagedFileBytes+1)
			},
		},
		{
			name: "metadata file size",
			mutate: func(request *HomeSkillPublishRequest) {
				request.Metadata.Metadata = map[string]any{"created_by": strings.Repeat("x", maxManagedFileBytes+1)}
			},
		},
		{
			name: "total includes metadata",
			mutate: func(request *HomeSkillPublishRequest) {
				request.Files = request.Files[:1]
				for i := range 4 {
					request.Files = append(request.Files, HomeSkillFile{Path: fmt.Sprintf("large-%d", i), Content: make([]byte, maxManagedFileBytes), Mode: 0o644})
				}
			},
		},
		{
			name: "path components",
			mutate: func(request *HomeSkillPublishRequest) {
				request.Files[1].Path = strings.Repeat("part/", maxManagedTreeDepth) + "file"
			},
		},
		{
			name: "SKILL.md mode",
			mutate: func(request *HomeSkillPublishRequest) {
				request.Files[0].Mode = 0o600
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := validHomeSkillRequest(root)
			tc.mutate(&request)
			if _, err := publisher.Publish(context.Background(), request); err == nil {
				t.Fatal("out-of-bounds publication succeeded")
			}
			homes.mu.Lock()
			opens := len(homes.used)
			homes.mu.Unlock()
			if opens != 0 {
				t.Fatalf("out-of-bounds publication opened Home %d times", opens)
			}
		})
	}
}

func TestHomeSkillPublisherCancellationAndUnknownOutcome(t *testing.T) {
	root := home.SystemSkillCatalog()
	publisher, homes := testHomeSkillPublisher(t, root)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := publisher.Publish(cancelled, validHomeSkillRequest(root)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled publish = %v", err)
	}
	homes.mu.Lock()
	if len(homes.used) != 0 {
		t.Fatal("cancelled publication opened Home")
	}
	homes.mu.Unlock()

	prePublish := errors.New("Home unavailable")
	homes.err = prePublish
	if _, err := publisher.Publish(context.Background(), validHomeSkillRequest(root)); !errors.Is(err, prePublish) || errors.Is(err, sandbox.ErrOutcomeUnknown) {
		t.Fatalf("pre-publication error = %v", err)
	}
	homes.err = nil
	unknown := &genericErrorPublishFilesystem{Filesystem: homes.filesystems[root], err: errors.New("publisher disconnected")}
	publisher.homes = homeSkillFilesystemAccessFunc(func(_ context.Context, _ *home.SkillRoot, use func(sandbox.Filesystem) error) error {
		return use(unknown)
	})
	if _, err := publisher.Publish(context.Background(), validHomeSkillRequest(root)); !errors.Is(err, sandbox.ErrOutcomeUnknown) || unknown.publishes != 1 {
		t.Fatalf("post-publication error = %v, publishes=%d", err, unknown.publishes)
	}
	mismatch := &mismatchedSelectionFilesystem{Filesystem: homes.filesystems[root]}
	publisher.homes = homeSkillFilesystemAccessFunc(func(_ context.Context, _ *home.SkillRoot, use func(sandbox.Filesystem) error) error {
		return use(mismatch)
	})
	if _, err := publisher.Publish(context.Background(), validHomeSkillRequest(root)); !errors.Is(err, sandbox.ErrOutcomeUnknown) || errors.Is(err, ErrHomeSkillConflict) || mismatch.publishes != 1 || mismatch.inspections != 2 {
		t.Fatalf("post-publication mismatch = %v, publishes=%d inspections=%d", err, mismatch.publishes, mismatch.inspections)
	}
}

func TestHomeSkillPublisherMarksCallbackReleaseAfterPublicationUnknown(t *testing.T) {
	root := home.SystemSkillCatalog()
	publisher, homes := testHomeSkillPublisher(t, root)
	closeErr := errors.New("filesystem close failed")
	publisher.homes = homeSkillFilesystemAccessFunc(func(_ context.Context, _ *home.SkillRoot, use func(sandbox.Filesystem) error) error {
		if err := use(homes.filesystems[root]); err != nil {
			return err
		}
		return closeErr
	})
	request := validHomeSkillRequest(root)
	if _, err := publisher.Publish(context.Background(), request); !errors.Is(err, sandbox.ErrOutcomeUnknown) || !errors.Is(err, closeErr) {
		t.Fatalf("post-publication release error = %v", err)
	}
	target, err := homes.filesystems[root].(sandbox.ManagedSkillTargetInspector).InspectManagedSkillTarget(context.Background(), path.Join(sandbox.PathWorkspace, request.Name))
	if err != nil || !target.Managed {
		t.Fatalf("target after release error = %+v, %v", target, err)
	}
}

func TestHomeSkillPublisherReturnsCancellationFromInitialInspection(t *testing.T) {
	root := home.SystemSkillCatalog()
	publisher, homes := testHomeSkillPublisher(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	publisher.homes = homeSkillFilesystemAccessFunc(func(_ context.Context, _ *home.SkillRoot, use func(sandbox.Filesystem) error) error {
		return use(&cancelledInspectionFilesystem{Filesystem: homes.filesystems[root], cancel: cancel})
	})
	if _, err := publisher.Publish(ctx, validHomeSkillRequest(root)); !errors.Is(err, context.Canceled) || errors.Is(err, ErrHomeSkillConflict) {
		t.Fatalf("cancelled initial inspection = %v", err)
	}
	publisher.homes = homeSkillFilesystemAccessFunc(func(_ context.Context, _ *home.SkillRoot, use func(sandbox.Filesystem) error) error {
		return use(&cancelledInspectionFilesystem{Filesystem: homes.filesystems[root], err: sandbox.ErrOutcomeUnknown})
	})
	if _, err := publisher.Publish(context.Background(), validHomeSkillRequest(root)); !errors.Is(err, sandbox.ErrOutcomeUnknown) {
		t.Fatalf("unknown initial inspection = %v", err)
	}
}

func TestHomeSkillPublisherSerializesSameRootCreate(t *testing.T) {
	root := home.SystemSkillCatalog()
	publisher, _ := testHomeSkillPublisher(t, root)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Go(func() {
			<-start
			_, err := publisher.Publish(context.Background(), validHomeSkillRequest(root))
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
		case errors.Is(err, ErrHomeSkillConflict):
			conflicts++
		default:
			t.Fatalf("unexpected contention result: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("contention successes/conflicts = %d/%d, want 1/1", successes, conflicts)
	}
}

type homeSkillFilesystemAccessFunc func(context.Context, *home.SkillRoot, func(sandbox.Filesystem) error) error

func (f homeSkillFilesystemAccessFunc) UseSkillFilesystem(ctx context.Context, root *home.SkillRoot, use func(sandbox.Filesystem) error) error {
	return f(ctx, root, use)
}

type cancelledInspectionFilesystem struct {
	sandbox.Filesystem
	cancel context.CancelFunc
	err    error
}

func (f *cancelledInspectionFilesystem) InspectManagedSkillTarget(context.Context, string) (sandbox.ManagedSkillTarget, error) {
	if f.cancel != nil {
		f.cancel()
	}
	if f.err != nil {
		return sandbox.ManagedSkillTarget{}, f.err
	}
	return sandbox.ManagedSkillTarget{}, errors.New("inspection interrupted")
}

func (f *cancelledInspectionFilesystem) PublishManagedSkill(ctx context.Context, root, name, digest string, publication sandbox.ManagedSkillPublication) error {
	return f.Filesystem.(sandbox.ManagedSkillPublisher).PublishManagedSkill(ctx, root, name, digest, publication)
}
