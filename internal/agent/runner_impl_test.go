package agent

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/fsops"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/renderrefs"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/pkg/tools"
)

type stubProvider struct{}

type delegatePresetSpyFS struct {
	pkgsandbox.Filesystem
	closeErr error
	closes   int
}

func (f *delegatePresetSpyFS) Close() error { f.closes++; return f.closeErr }

type delegatePresetErrorFS struct {
	*delegatePresetSpyFS
	listErr error
}

func (f *delegatePresetErrorFS) List(context.Context, string) ([]pkgsandbox.DirEntry, error) {
	return nil, f.listErr
}

type delegatePresetSession struct {
	*fakeSession
	filesystem    pkgsandbox.Filesystem
	filesystemErr error
}

func (s *delegatePresetSession) WorkingDir() string { return pkgsandbox.PathWorkspace }
func (s *delegatePresetSession) Filesystem() (pkgsandbox.Filesystem, error) {
	return s.filesystem, s.filesystemErr
}

type runtimeProjectTestSession struct {
	pkgsandbox.Session
	root string
}

func (s runtimeProjectTestSession) WorkingDir() string                       { return s.root }
func (runtimeProjectTestSession) Filesystem() (pkgsandbox.Filesystem, error) { return nil, nil }
func (s runtimeProjectTestSession) FilesystemWorkingDirectory() (string, bool) {
	return s.root, true
}

type runtimeProjectProjectorSession struct {
	runtimeProjectTestSession
	projected string
	ok        bool
	input     string
}

func (s *runtimeProjectProjectorSession) FilesystemWorkingDirectory() (string, bool) {
	input := s.WorkingDir()
	s.input = input
	return s.projected, s.ok
}

type stubTool struct{ name string }

func (s *stubTool) Definition() tools.Definition {
	return tools.Definition{Name: s.name, Description: "stub tool"}
}

func (s *stubTool) Execute(context.Context, map[string]any) (string, error) {
	return s.name, nil
}

func (s *stubProvider) API() string { return "anthropic" }
func (s *stubProvider) Stream(context.Context, ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
	return nil, errors.New("stub")
}

func testProviderStreamBuilder(api, apiKey, baseURL string) (providers.StreamFunc, error) {
	if api != "anthropic" {
		return nil, providers.ErrProviderNotFound
	}
	return providers.AdapterStreamFunc(&stubProvider{}), nil
}

func TestRunnerAliveWithoutSandboxOnlyForNoCapabilities(t *testing.T) {
	if (&runner{}).Alive() {
		t.Fatal("runner with an unexpectedly missing sandbox session reported alive")
	}
	if !(&runner{noCapabilities: true}).Alive() {
		t.Fatal("no-capabilities runner was treated as dead without a sandbox session")
	}
	if (&runner{session: &fakeSession{alive: false}}).Alive() {
		t.Fatal("runner ignored a dead sandbox session")
	}
	if !(&runner{session: &fakeSession{alive: true}}).Alive() {
		t.Fatal("runner ignored a live sandbox session")
	}
}

func testRunnerPaths(t *testing.T) (stellaHome, workspace, userRoot string) {
	t.Helper()
	stellaHome = t.TempDir()
	workspace = t.TempDir()
	userRoot = filepath.Join(workspace, "users", "1")
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return stellaHome, workspace, userRoot
}

func withTestRunnerPaths(t *testing.T, cfg runnerConfig) runnerConfig {
	t.Helper()
	stellaHome, workspace, userRoot := testRunnerPaths(t)
	cfg.Sandbox.Paths.StellaHome = stellaHome
	cfg.Sandbox.Paths.AgentRoot = workspace
	cfg.Sandbox.Paths.UserRoot = userRoot
	return cfg
}

func TestFilterRunnerTools(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "zeta"})
	reg.Register(&stubTool{name: "middle"})
	reg.Register(&stubTool{name: "alpha"})

	set, defs, err := filterRunnerTools(reg, []string{"middle", "not-registered"})
	if err != nil {
		t.Fatalf("filterRunnerTools: %v", err)
	}
	if len(defs) != 2 || defs[0].Name != "alpha" || defs[1].Name != "zeta" {
		t.Fatalf("defs = %#v, want alpha then zeta", defs)
	}
	if len(set) != 2 {
		t.Fatalf("tool set length = %d, want 2", len(set))
	}
	if _, ok := set["middle"]; ok {
		t.Fatal("middle should be excluded")
	}
	if _, ok := set["alpha"]; !ok {
		t.Fatal("alpha should remain available")
	}
	if _, ok := set["zeta"]; !ok {
		t.Fatal("zeta should remain available")
	}

	result, err := set["alpha"](context.Background(), ai.ToolCall{Name: "alpha"})
	if err != nil {
		t.Fatalf("execute filtered alpha tool: %v", err)
	}
	if ai.FlattenText(result) != "alpha" {
		t.Fatalf("filtered alpha result = %q, want alpha", ai.FlattenText(result))
	}
}

func TestGroupSkillRuntimeBoundaryExcludesPrincipalTiers(t *testing.T) {
	paths := sandbox.Paths{
		StellaHome: "/stella", BuiltinBundle: "/bundle", AgentRoot: "/agent-definition",
		UserDataDir: "/group/data", WorkspaceRoot: "/group/agents/a", ProjectRoot: "/group/agents/a/projects/p",
	}
	view := skillRuntimeView(context.Background(), runnerConfig{BuiltinParams: RunnerParams{GroupID: "g"}}, paths)
	if view.UserDataHost != "" || view.WorkspaceHost != "" {
		t.Fatalf("group model Skill view exposes principal mounts: %+v", view)
	}
	if view.SystemDBSkillsView == "" || view.AgentSkillsView == "" || view.BuiltinSkillsHost == "" || paths.ProjectRoot == "" {
		t.Fatalf("group lost retained Skill/project tiers: view=%+v project=%q", view, paths.ProjectRoot)
	}
}

func TestRuntimeProjectSkillRootUsesSessionCoordinate(t *testing.T) {
	paths := sandbox.Paths{ProjectRoot: "/host/private/project", WorkDir: "/host/private/project"}
	root, err := runtimeProjectSkillRoot(paths, runtimeProjectTestSession{Session: pkgsandbox.NopSession(), root: "/workspace/projects/p"})
	if err != nil || root != "/workspace/projects/p" {
		t.Fatalf("root = %q, %v", root, err)
	}
	for _, root := range []string{"/host/private/project", "/user/project", "/workspace/../escape"} {
		if _, err := runtimeProjectSkillRoot(paths, runtimeProjectTestSession{Session: pkgsandbox.NopSession(), root: root}); err == nil {
			t.Fatalf("accepted %q", root)
		}
	}
	projected := &runtimeProjectProjectorSession{runtimeProjectTestSession: runtimeProjectTestSession{Session: pkgsandbox.NopSession(), root: "/tmp/host-project"}, projected: "/workspace/projects/p", ok: true}
	root, err = runtimeProjectSkillRoot(paths, projected)
	if err != nil || root != "/workspace/projects/p" || projected.input != "/tmp/host-project" {
		t.Fatalf("projection root=%q err=%v input=%q", root, err, projected.input)
	}
	for _, projectedRoot := range []string{"/user/project", "/workspace/../escape"} {
		s := &runtimeProjectProjectorSession{runtimeProjectTestSession: runtimeProjectTestSession{Session: pkgsandbox.NopSession(), root: "/host/process/project"}, projected: projectedRoot, ok: true}
		if _, err := runtimeProjectSkillRoot(paths, s); err == nil {
			t.Fatalf("accepted projection %q", projectedRoot)
		}
	}
	wrapped := pkgsandbox.NewResilientSession(&runtimeProjectProjectorSession{runtimeProjectTestSession: runtimeProjectTestSession{Session: pkgsandbox.NopSession(), root: "/host/process/project"}, projected: "/workspace/projects/p", ok: true}, func(context.Context) (pkgsandbox.Session, error) {
		t.Fatal("projection must not recreate")
		return nil, nil
	})
	if root, err := runtimeProjectSkillRoot(paths, wrapped); err != nil || root != "/workspace/projects/p" {
		t.Fatalf("resilient projection root=%q err=%v", root, err)
	}
	if _, err := runtimeProjectSkillRoot(paths, &runtimeProjectProjectorSession{runtimeProjectTestSession: runtimeProjectTestSession{Session: pkgsandbox.NopSession(), root: "/host/process/project"}}); err == nil {
		t.Fatal("accepted failed projection")
	}
}

func TestBuildDelegatePresetsFilesystemBoundary(t *testing.T) {
	t.Parallel()
	home, workspace, userRoot := testRunnerPaths(t)
	cfg := runnerConfig{Sandbox: sandbox.Config{Paths: sandbox.Paths{StellaHome: home, AgentRoot: workspace, UserRoot: userRoot}}, BuiltinParams: RunnerParams{UserID: "u", AgentID: "a"}}
	newFS := func() *delegatePresetSpyFS {
		filesystem, err := fsops.NewFilesystem([]fsops.Mount{
			{Path: pkgsandbox.PathWorkspace, Directory: t.TempDir()},
			{Path: pkgsandbox.PathUser, Directory: t.TempDir()},
		})
		if err != nil {
			t.Fatal(err)
		}
		return &delegatePresetSpyFS{Filesystem: filesystem}
	}
	spy := newFS()
	session := &delegatePresetSession{fakeSession: &fakeSession{alive: true}, filesystem: spy}
	if _, err := buildDelegatePresets(context.Background(), cfg, session); err != nil {
		t.Fatalf("buildDelegatePresets: %v", err)
	}
	if spy.closes != 1 {
		t.Fatalf("filesystem closes = %d", spy.closes)
	}

	acquireErr := &delegatePresetSession{fakeSession: &fakeSession{alive: true}, filesystemErr: errors.New("acquire")}
	if _, err := buildDelegatePresets(context.Background(), cfg, acquireErr); err == nil {
		t.Fatal("acquisition error accepted")
	}
	nilFS := &delegatePresetSession{fakeSession: &fakeSession{alive: true}}
	if _, err := buildDelegatePresets(context.Background(), cfg, nilFS); err == nil {
		t.Fatal("nil filesystem accepted")
	}

	loaderSpy := newFS()
	loaderSpy.closeErr = errors.New("close")
	loaderFailure := &delegatePresetErrorFS{delegatePresetSpyFS: loaderSpy, listErr: errors.New("list")}
	loaderSession := &delegatePresetSession{fakeSession: &fakeSession{alive: true}, filesystem: loaderFailure}
	if _, err := buildDelegatePresets(context.Background(), cfg, loaderSession); !errors.Is(err, loaderFailure.listErr) || !errors.Is(err, loaderSpy.closeErr) {
		t.Fatalf("loader/close errors not joined: %v", err)
	}
	if loaderSpy.closes != 1 {
		t.Fatalf("failed filesystem closes = %d", loaderSpy.closes)
	}

	creates := 0
	resilient := pkgsandbox.NewResilientSession(session, func(context.Context) (pkgsandbox.Session, error) {
		creates++
		return nil, errors.New("must not recreate")
	})
	if _, err := buildDelegatePresets(context.Background(), cfg, resilient); err != nil {
		t.Fatal(err)
	}
	if creates != 0 {
		t.Fatalf("resilient session recreated %d times", creates)
	}
}

func TestBuildDelegatePresetsRuntimeOnlyASTGuard(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	runnerFile := strings.TrimSuffix(filename, "_test.go") + ".go"
	file, err := parser.ParseFile(token.NewFileSet(), runnerFile, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var target *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "buildDelegatePresets" {
			target = fn
			break
		}
	}
	if target == nil {
		t.Fatal("buildDelegatePresets missing")
	}
	var filesystem, runtimeLoader bool
	forbidden := map[string]bool{"ExtractDelegates": true, "LoadDelegatePresets": true, "stellaDelegatesDir": true}
	ast.Inspect(target.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if forbidden[selector.Sel.Name] {
			t.Errorf("forbidden host loader call %s", selector.Sel.Name)
		}
		filesystem = filesystem || selector.Sel.Name == "Filesystem"
		runtimeLoader = runtimeLoader || selector.Sel.Name == "LoadRuntimeDelegatePresets"
		if selector.Sel.Name == "LoadRuntimeDelegatePresets" {
			ast.Inspect(call, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				base, isPaths := sel.X.(*ast.Ident)
				if isPaths && base.Name == "paths" && (sel.Sel.Name == "StellaHome" || sel.Sel.Name == "AgentRoot" || sel.Sel.Name == "UserDataDir" || sel.Sel.Name == "ProjectRoot") {
					t.Errorf("host path field passed to runtime loader: %s", sel.Sel.Name)
				}
				return true
			})
		}
		return true
	})
	if !filesystem || !runtimeLoader {
		t.Fatalf("runtime wiring missing: Filesystem=%v loader=%v", filesystem, runtimeLoader)
	}
	pathsFile, err := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(runnerFile), "paths.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range pathsFile.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "stellaDelegatesDir" {
			t.Fatal("obsolete stellaDelegatesDir remains")
		}
	}
}

func TestBuildToolRegistrySkillsToolDoesNotReceiveHostProjectRoot(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	file, err := parser.ParseFile(token.NewFileSet(), strings.TrimSuffix(filename, "_test.go")+".go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var build *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "buildToolRegistry" {
			build = fn
			break
		}
	}
	if build == nil {
		t.Fatal("buildToolRegistry missing")
	}
	var emptyRoot, newTool, runtimeFilesystem bool
	ast.Inspect(build.Body, func(n ast.Node) bool {
		if assign, ok := n.(*ast.AssignStmt); ok {
			for i, lhs := range assign.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok && ident.Name == "toolProjectRoot" && i < len(assign.Rhs) {
					if literal, ok := assign.Rhs[i].(*ast.BasicLit); ok && literal.Kind == token.STRING && literal.Value == `""` {
						emptyRoot = true
					}
				}
			}
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if selector.Sel.Name == "WithRuntimeFilesystem" {
			runtimeFilesystem = true
		}
		if selector.Sel.Name == "NewTool" && len(call.Args) == 3 {
			if ident, ok := call.Args[2].(*ast.Ident); ok && ident.Name == "toolProjectRoot" {
				newTool = true
			}
		}
		return true
	})
	if !emptyRoot || !newTool || !runtimeFilesystem {
		t.Fatalf("skills wiring changed: empty=%v NewTool=%v runtime=%v", emptyRoot, newTool, runtimeFilesystem)
	}
}

func TestNewRunnerRequiresConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  runnerConfig
	}{
		{"missing api", runnerConfig{Provider: providerConfig{Model: "m", APIKey: "k"}}},
		{"missing model", runnerConfig{Provider: providerConfig{API: "anthropic", APIKey: "k"}}},
		{"missing api_key", runnerConfig{Provider: providerConfig{API: "anthropic", Model: "m"}}},
		{"missing workspace", runnerConfig{Provider: providerConfig{API: "anthropic", Model: "m", APIKey: "k"}, Sandbox: sandbox.Config{Paths: sandbox.Paths{UserRoot: "/tmp/user"}}}},
		{"missing user_data_dir", runnerConfig{Provider: providerConfig{API: "anthropic", Model: "m", APIKey: "k"}, Sandbox: sandbox.Config{Paths: sandbox.Paths{AgentRoot: "/tmp/workspace"}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newRunner(context.Background(), tt.cfg)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// runnerFakeProvider implements stream.Provider for testing Chat() without real API calls.
type runnerFakeProvider struct {
	api    string
	events []ai.AssistantEvent
	err    error
}

func (f *runnerFakeProvider) API() string { return f.api }

func (f *runnerFakeProvider) Stream(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := providers.NewChannelEventStream(len(f.events) + 1)
	go func() {
		for _, evt := range f.events {
			out.Emit(evt)
		}
		out.Finish(nil)
	}()
	return out, nil
}

// newTestRunner creates a runner wired to a fake provider.
// Requires a reachable docker daemon since docker is now the only sandbox backend.
// Skips the test if the docker daemon is not reachable or container creation fails.
func newTestRunner(t *testing.T, fp *runnerFakeProvider) *runner {
	t.Helper()
	builder := func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
		if api != fp.api {
			return nil, providers.ErrProviderNotFound
		}
		return providers.AdapterStreamFunc(fp), nil
	}
	r, err := newRunner(context.Background(), withTestRunnerPaths(t, runnerConfig{
		Provider: providerConfig{
			API:     fp.api,
			Model:   "test-model",
			APIKey:  "test-key",
			Builder: builder,
		},
	}))
	if err != nil {
		t.Skipf("newRunner: docker not available: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestChatStreamsTextDeltas(t *testing.T) {
	fp := &runnerFakeProvider{
		api: "anthropic",
		events: []ai.AssistantEvent{
			ai.EventStart{},
			ai.EventTextDelta{Text: "Hello "},
			ai.EventTextDelta{Text: "world"},
			ai.EventStop{Reason: ai.StopReasonStop},
		},
	}
	r := newTestRunner(t, fp)

	ch := r.Chat(context.Background(), nil, "hi")

	var collected string
	for evt := range ch {
		if evt.Err != nil {
			t.Fatalf("unexpected error: %v", evt.Err)
		}
		collected += evt.Text
	}

	if collected != "Hello world" {
		t.Errorf("collected = %q, want %q", collected, "Hello world")
	}
}

func TestChatStreamError(t *testing.T) {
	fp := &runnerFakeProvider{
		api: "anthropic",
		err: errors.New("provider boom"),
	}
	r := newTestRunner(t, fp)

	ch := r.Chat(context.Background(), nil, "hi")

	var gotErr error
	for evt := range ch {
		if evt.Err != nil {
			gotErr = evt.Err
			break
		}
	}

	if gotErr == nil {
		t.Fatal("expected error from stream")
	}
}

func TestChatUnknownProvider(t *testing.T) {
	_, err := newRunner(context.Background(), withTestRunnerPaths(t, runnerConfig{
		Provider: providerConfig{
			API:     "nonexistent",
			Model:   "test-model",
			APIKey:  "test-key",
			Builder: testProviderStreamBuilder,
		},
	}))
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestChatContextCancellation(t *testing.T) {
	fp := &runnerFakeProvider{
		api: "anthropic",
		events: []ai.AssistantEvent{
			ai.EventTextDelta{Text: "ok"},
			ai.EventStop{Reason: ai.StopReasonStop},
		},
	}
	r := newTestRunner(t, fp)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ch := r.Chat(ctx, nil, "hi")

	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Chat channel did not close after context cancellation")
	}
}

func TestLastActivityUpdatesOnChat(t *testing.T) {
	fp := &runnerFakeProvider{
		api: "anthropic",
		events: []ai.AssistantEvent{
			ai.EventTextDelta{Text: "ok"},
			ai.EventStop{Reason: ai.StopReasonStop},
		},
	}
	r := newTestRunner(t, fp)

	before := time.Now()
	time.Sleep(1 * time.Millisecond)

	ch := r.Chat(context.Background(), nil, "hi")
	for range ch {
	}

	if r.LastActivity().Before(before) {
		t.Errorf("LastActivity %v should be after %v", r.LastActivity(), before)
	}
}

func TestConvertLoopEventStripsMalformedSentinelFromStore(t *testing.T) {
	// A truncated/corrupt sentinel yields no ref, but the raw marker must still be
	// scrubbed from the persisted result so a replay never feeds it to the model.
	text := "created task\n::stella-ref/v1::{\"v\":1,\"type\":\"ta"

	events := convertLoopEvent(coreagent.ToolFinished{Result: ai.ToolResultMessage{
		ToolCallID: "call-1",
		ToolName:   "bash",
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
	}})
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if len(events[0].References) != 0 {
		t.Fatalf("malformed sentinel produced refs: %#v", events[0].References)
	}
	stored, ok := events[1].Store.(ai.ToolResultMessage)
	if !ok {
		t.Fatalf("second event Store = %T, want ai.ToolResultMessage", events[1].Store)
	}
	for _, block := range stored.Content {
		if tc, ok := block.(ai.TextContent); ok && strings.Contains(tc.Text, "::stella-ref/v1::") {
			t.Fatalf("stored result leaked malformed sentinel: %q", tc.Text)
		}
	}
}

func TestConvertLoopEventStripsRenderableReferences(t *testing.T) {
	ref := renderrefs.Reference{
		V:    1,
		Type: "task",
		ID:   "task-1",
		Preview: &renderrefs.Preview{
			Title:  "Ship it",
			Status: "open",
		},
	}
	var sb strings.Builder
	if err := renderrefs.Emit(&sb, ref); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	text := "created task\n" + sb.String()

	events := convertLoopEvent(coreagent.ToolFinished{Result: ai.ToolResultMessage{
		ToolCallID: "call-1",
		ToolName:   "bash",
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
	}})
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].ToolUse == nil {
		t.Fatal("first event missing tool use")
	}
	if strings.Contains(events[0].ToolUse.Content, "::stella-ref/v1::") {
		t.Fatalf("tool content leaked sentinel: %q", events[0].ToolUse.Content)
	}
	// References live only on the tool event now; the event-level field is fanned
	// out later by the coordinator, not set here.
	if events[0].References != nil {
		t.Fatalf("event-level references should be unset, got %#v", events[0].References)
	}
	if len(events[0].ToolUse.References) != 1 || events[0].ToolUse.References[0].ID != "task-1" {
		t.Fatalf("tool references = %#v", events[0].ToolUse.References)
	}

	// The persisted tool result must be stripped too, or a replay would feed the
	// sentinel back to the model.
	stored, ok := events[1].Store.(ai.ToolResultMessage)
	if !ok {
		t.Fatalf("second event Store = %T, want ai.ToolResultMessage", events[1].Store)
	}
	for _, block := range stored.Content {
		if tc, ok := block.(ai.TextContent); ok && strings.Contains(tc.Text, "::stella-ref/v1::") {
			t.Fatalf("stored result leaked sentinel: %q", tc.Text)
		}
	}
	if len(stored.References) != 1 || stored.References[0].ID != "task-1" {
		t.Fatalf("stored references = %#v", stored.References)
	}
}
