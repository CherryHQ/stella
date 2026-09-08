package plugin

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

type casRootOpener struct {
	delegate home.RootOpener
	role     string
	gate     *casGate
}

type casGate struct {
	mu            sync.Mutex
	readOpens     int
	readCloses    int
	readsReady    chan struct{}
	readyOnce     sync.Once
	deleteOnce    sync.Once
	deleteStarted chan struct{}
	patchOnce     sync.Once
	patchClosed   chan struct{}
}

func (o *casRootOpener) OpenRoot(ctx context.Context, request home.WorkspaceRequest, scope home.RootScope, access home.RootAccess) (home.RootOperations, error) {
	if o.role == "delete" {
		o.gate.deleteOnce.Do(func() { close(o.gate.deleteStarted) })
	}
	if access == home.RootReadOnly {
		o.gate.mu.Lock()
		o.gate.readOpens++
		o.gate.mu.Unlock()
		root, err := o.delegate.OpenRoot(ctx, request, scope, access)
		if err != nil {
			return nil, err
		}
		return &casObservedRoot{RootOperations: root, onClose: o.gate.closeRead}, nil
	}
	if access == home.RootReadWrite {
		switch o.role {
		case "patch":
			select {
			case <-o.gate.deleteStarted:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			o.gate.mu.Lock()
			wait := o.gate.readOpens != 0
			o.gate.mu.Unlock()
			if wait {
				select {
				case <-o.gate.readsReady:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		case "delete":
			select {
			case <-o.gate.patchClosed:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	root, err := o.delegate.OpenRoot(ctx, request, scope, access)
	if err != nil {
		return nil, err
	}
	if access == home.RootReadWrite && o.role == "patch" {
		return &casObservedRoot{RootOperations: root, onClose: o.gate.closePatch}, nil
	}
	return root, nil
}

func (g *casGate) closeRead() {
	g.mu.Lock()
	g.readCloses++
	ready := g.readCloses >= 2
	g.mu.Unlock()
	if ready {
		g.readyOnce.Do(func() { close(g.readsReady) })
	}
}

func (g *casGate) closePatch() {
	g.patchOnce.Do(func() { close(g.patchClosed) })
}

type casObservedRoot struct {
	home.RootOperations
	onClose func()
	once    sync.Once
}

func (r *casObservedRoot) Close() error {
	err := r.RootOperations.Close()
	r.once.Do(r.onClose)
	return err
}

func TestResourceStoreCASKeepsReadAndMutationUnderOneRoot(t *testing.T) {
	db := dbtest.New(t)
	user := "00000000-0000-4000-8000-000000000322"
	if _, err := db.Exec(t.Context(), "INSERT INTO auth_user(id,email) VALUES($1,$2)", user, "resource-cas@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), "INSERT INTO agent(id,name,workspace) VALUES('resource-cas-agent','Resource CAS','')"); err != nil {
		t.Fatal(err)
	}
	manager, err := home.NewWorkspaceManager(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	key := ResourceKey{Scope: ScopeUserAgent, UserID: user, AgentID: "resource-cas-agent", Kind: ResourcePlugin, Name: "cas-demo"}
	manifest := []byte(`{"$schema":"` + agentpackage.PluginSchemaV1 + `","name":"cas-demo"}`)
	created, err := NewResourceStore(manager).CreatePlugin(t.Context(), key, map[string]ResourceFile{
		"plugin.json": {Data: manifest, Mode: 0o644},
		"bin/run":     {Data: []byte("echo hi"), Mode: 0o755},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The opener releases the stale Delete only after Patch closes its RW root.
	// It releases the former Get-then-open-RW implementation only after both
	// read-only roots close, so the old code validates both calls against the
	// same digest before Patch mutates. The fixed implementation never opens a
	// read-only root for these mutations.
	gate := &casGate{readsReady: make(chan struct{}), deleteStarted: make(chan struct{}), patchClosed: make(chan struct{})}
	patchStore := NewResourceStore(&casRootOpener{delegate: manager, role: "patch", gate: gate})
	deleteStore := NewResourceStore(&casRootOpener{delegate: manager, role: "delete", gate: gate})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	patchErrs := make(chan error, 1)
	deleteErrs := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Go(func() {
		<-start
		_, err := patchStore.PatchPlugin(ctx, key, created.Digest, map[string]*ResourceFile{
			"README.md": {Data: []byte("patched"), Mode: 0o644},
		})
		patchErrs <- err
	})
	wg.Go(func() {
		<-start
		deleteErrs <- deleteStore.Delete(ctx, key, created.Digest)
	})
	close(start)
	wg.Wait()
	patchErr := <-patchErrs
	deleteErr := <-deleteErrs
	if patchErr != nil {
		t.Fatalf("patch = %v", patchErr)
	}
	if !errors.Is(deleteErr, ErrConflict) {
		t.Fatalf("stale delete = %v, want ErrConflict", deleteErr)
	}
}

func TestResourceStorePluginCASAndSettings(t *testing.T) {
	db := dbtest.New(t)
	user := "00000000-0000-4000-8000-000000000321"
	if _, err := db.Exec(t.Context(), "INSERT INTO auth_user(id,email) VALUES($1,$2)", user, "resource@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), "INSERT INTO agent(id,name,workspace) VALUES('resource-agent','Resource','')"); err != nil {
		t.Fatal(err)
	}
	manager, err := home.NewWorkspaceManager(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	store := NewResourceStore(manager)
	key := ResourceKey{Scope: ScopeUserAgent, UserID: user, AgentID: "resource-agent", Kind: ResourcePlugin, Name: "demo"}
	manifest := []byte(`{"$schema":"` + agentpackage.PluginSchemaV1 + `","name":"demo"}`)
	created, err := store.CreatePlugin(t.Context(), key, map[string]ResourceFile{
		"plugin.json": {Data: manifest, Mode: 0o644},
		"bin/run":     {Data: []byte("echo hi"), Mode: 0o755},
	})
	if err != nil || created.Digest == "" || created.Package == nil {
		t.Fatalf("create = %#v, %v", created, err)
	}
	if got, digest, err := store.ReadFile(t.Context(), key, "bin/run"); err != nil || string(got.Data) != "echo hi" || got.Mode.Perm()&0o111 != 0o111 || digest != created.Digest {
		t.Fatalf("read file = %#v %q (resource %q) mode=%#o data=%q same=%v err=%v", got, digest, created.Digest, got.Mode.Perm(), got.Data, digest == created.Digest, err)
	}
	if _, _, err := store.ReadFile(t.Context(), key, "../escape"); err == nil {
		t.Fatal("path escape accepted")
	}
	updated, err := store.PatchPlugin(t.Context(), key, created.Digest, map[string]*ResourceFile{
		"README.md": {Data: []byte("hello"), Mode: 0o644},
		"bin/run":   nil,
	})
	if err != nil || updated.Digest == created.Digest {
		t.Fatalf("patch = %#v, %v", updated, err)
	}
	if _, err := store.PatchPlugin(t.Context(), key, created.Digest, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale patch = %v", err)
	}
	settings, settingsDigest, err := store.ReadSettings(t.Context(), ScopeUserAgent, user, "resource-agent")
	if err != nil || settingsDigest != "" || len(settings.Disabled) != 0 {
		t.Fatalf("settings = %#v %q %v", settings, settingsDigest, err)
	}
	settings, settingsDigest, err = store.SetEnabled(t.Context(), key, settingsDigest, false)
	if err != nil || !slices.Contains(settings.Disabled, "plugin:demo") || settingsDigest == "" {
		t.Fatalf("disable = %#v %q %v", settings, settingsDigest, err)
	}
	got, err := store.Get(t.Context(), key)
	if err != nil || !got.Disabled {
		t.Fatalf("disabled get = %#v, %v", got, err)
	}
	if _, _, err := store.SetEnabled(t.Context(), key, "", true); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale setting toggle = %v", err)
	}
	if _, _, err := store.SetEnabled(t.Context(), key, settingsDigest, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SetForbidden(t.Context(), key, "", true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("user prohibition = %v", err)
	}
	settings, settingsDigest, err = store.ReadSettings(t.Context(), ScopeUserAgent, user, "resource-agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SetEnabled(t.Context(), key, settingsDigest, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(t.Context(), key, updated.Digest); err != nil {
		t.Fatal(err)
	}
	settings, _, err = store.ReadSettings(t.Context(), ScopeUserAgent, user, "resource-agent")
	if err != nil || !slices.Contains(settings.Disabled, "plugin:demo") {
		t.Fatalf("delete erased policy = %#v, %v", settings, err)
	}

	mcpKey := ResourceKey{Scope: ScopeUserAgent, UserID: user, AgentID: "resource-agent", Kind: ResourceMCP, Name: "remote"}
	mcpData := []byte(`{"url":"https://example.test/mcp","transport":"streamable_http","auth_type":"none"}`)
	mcp, err := store.WriteMCP(t.Context(), mcpKey, "", mcpData)
	if err != nil || mcp.Digest != digestBytes(mcpData) {
		t.Fatalf("write MCP = %#v, %v", mcp, err)
	}
	if got, digest, err := store.ReadMCP(t.Context(), mcpKey); err != nil || string(got) != string(mcpData) || digest != mcp.Digest {
		t.Fatalf("read MCP = %q %q %v", got, digest, err)
	}
	if err := store.Delete(t.Context(), mcpKey, mcp.Digest); err != nil {
		t.Fatal(err)
	}
	root, err := manager.OpenRoot(t.Context(), home.WorkspaceRequest{UserID: user, AgentID: "resource-agent"}, home.RootUserAgentResources, home.RootReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Mkdir(t.Context(), "mcp", 0o755, home.MkdirOptions{Parents: true}); err != nil {
		t.Fatal(err)
	}
	if err := root.Write(t.Context(), "mcp/bad.json", strings.NewReader("not-json"), home.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	malformed, err := store.ListScope(t.Context(), ScopeUserAgent, user, "resource-agent", ResourceMCP)
	if err != nil || len(malformed) != 1 || len(malformed[0].Diagnostics) == 0 {
		t.Fatalf("malformed MCP listing = %#v, %v", malformed, err)
	}
	if _, err := store.Get(t.Context(), ResourceKey{Scope: ScopeUserAgent, UserID: user, AgentID: "resource-agent", Kind: ResourcePlugin, Name: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
}
