package jsrt

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	pluginapi "github.com/vaayne/anna/pkg/plugin"
)

// testRegistry implements ToolHookRegistrar for testing.
type testRegistry struct {
	mu    sync.Mutex
	tools map[string]pluginapi.Tool
	hooks map[pluginapi.EventKind][]pluginapi.HookFunc
}

func newTestRegistry() *testRegistry {
	return &testRegistry{
		tools: make(map[string]pluginapi.Tool),
		hooks: make(map[pluginapi.EventKind][]pluginapi.HookFunc),
	}
}

func (r *testRegistry) RegisterTool(t pluginapi.Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name] = t
	return nil
}

func (r *testRegistry) RegisterHook(event pluginapi.EventKind, fn pluginapi.HookFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hooks[event] = append(r.hooks[event], fn)
}

func TestLoadJS_ToolRegistration(t *testing.T) {
	dir := t.TempDir()
	pluginFile := filepath.Join(dir, "hello.js")
	code := `
		anna.registerTool({
			name: "hello_world",
			description: "Says hello",
			parameters: { type: "object", properties: { name: { type: "string" } } },
			execute: function(args) {
				return "Hello, " + args.name + "!";
			}
		});
	`
	if err := os.WriteFile(pluginFile, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := newTestRegistry()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	p, err := LoadJS(pluginFile, nil, reg, logger)
	if err != nil {
		t.Fatalf("LoadJS: %v", err)
	}
	defer func() { _ = p.Close() }()

	if p.Name() != "hello" {
		t.Errorf("Name() = %q, want %q", p.Name(), "hello")
	}

	tool, ok := reg.tools["hello_world"]
	if !ok {
		t.Fatal("tool hello_world not registered")
	}

	result, err := tool.Execute(context.Background(), map[string]any{"name": "World"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "Hello, World!" {
		t.Errorf("Execute result = %q, want %q", result, "Hello, World!")
	}
}

func TestLoadJS_HookRegistration(t *testing.T) {
	dir := t.TempDir()
	pluginFile := filepath.Join(dir, "hooks.js")
	code := `
		anna.on("before_tool_call", function(event) {
			if (event.toolName === "blocked_tool") {
				return "tool is blocked by plugin";
			}
		});
	`
	if err := os.WriteFile(pluginFile, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := newTestRegistry()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	p, err := LoadJS(pluginFile, nil, reg, logger)
	if err != nil {
		t.Fatalf("LoadJS: %v", err)
	}
	defer func() { _ = p.Close() }()

	hooks := reg.hooks[pluginapi.EventBeforeToolCall]
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(hooks))
	}

	// Should block the "blocked_tool".
	err = hooks[0](context.Background(), pluginapi.BeforeToolCallEvent{
		ToolName: "blocked_tool",
	})
	if err == nil {
		t.Fatal("expected hook to return error for blocked_tool")
	}
	if err.Error() != "tool is blocked by plugin" {
		t.Errorf("error = %q, want %q", err.Error(), "tool is blocked by plugin")
	}

	// Should allow other tools.
	err = hooks[0](context.Background(), pluginapi.BeforeToolCallEvent{
		ToolName: "allowed_tool",
	})
	if err != nil {
		t.Errorf("expected nil error for allowed_tool, got: %v", err)
	}
}

func TestLoadJS_Config(t *testing.T) {
	dir := t.TempDir()
	pluginFile := filepath.Join(dir, "config_test.js")
	code := `
		anna.registerTool({
			name: "config_echo",
			description: "Echoes config",
			parameters: {},
			execute: function(args) {
				return "key=" + anna.config.api_key;
			}
		});
	`
	if err := os.WriteFile(pluginFile, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := newTestRegistry()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	cfg := map[string]any{"api_key": "secret123"}
	p, err := LoadJS(pluginFile, cfg, reg, logger)
	if err != nil {
		t.Fatalf("LoadJS: %v", err)
	}
	defer func() { _ = p.Close() }()

	tool := reg.tools["config_echo"]
	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "key=secret123" {
		t.Errorf("result = %q, want %q", result, "key=secret123")
	}
}

func TestLoadJS_SyntaxError(t *testing.T) {
	dir := t.TempDir()
	pluginFile := filepath.Join(dir, "bad.js")
	if err := os.WriteFile(pluginFile, []byte("this is not valid javascript {{{"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := newTestRegistry()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	_, err := LoadJS(pluginFile, nil, reg, logger)
	if err == nil {
		t.Fatal("expected error for syntax error in JS")
	}
}

func TestLoadJS_HostAPI_ReadFile(t *testing.T) {
	dir := t.TempDir()

	// Create a data file in the plugin directory.
	dataFile := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(dataFile, []byte("hello from file"), 0o644); err != nil {
		t.Fatal(err)
	}

	pluginFile := filepath.Join(dir, "reader.js")
	code := `
		anna.registerTool({
			name: "read_data",
			description: "Reads data.txt",
			parameters: {},
			execute: function(args) {
				return anna.readFile("data.txt");
			}
		});
	`
	if err := os.WriteFile(pluginFile, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := newTestRegistry()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	p, err := LoadJS(pluginFile, nil, reg, logger)
	if err != nil {
		t.Fatalf("LoadJS: %v", err)
	}
	defer func() { _ = p.Close() }()

	tool := reg.tools["read_data"]
	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "hello from file" {
		t.Errorf("result = %q, want %q", result, "hello from file")
	}
}

func TestLoadJS_HostAPI_WriteFile(t *testing.T) {
	dir := t.TempDir()

	pluginFile := filepath.Join(dir, "writer.js")
	code := `
		anna.registerTool({
			name: "write_data",
			description: "Writes output.txt",
			parameters: {},
			execute: function(args) {
				anna.writeFile("output.txt", "written by plugin");
				return "done";
			}
		});
	`
	if err := os.WriteFile(pluginFile, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := newTestRegistry()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	p, err := LoadJS(pluginFile, nil, reg, logger)
	if err != nil {
		t.Fatalf("LoadJS: %v", err)
	}
	defer func() { _ = p.Close() }()

	tool := reg.tools["write_data"]
	_, err = tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "output.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "written by plugin" {
		t.Errorf("file content = %q, want %q", string(data), "written by plugin")
	}
}

func TestLoadJS_PathEscape(t *testing.T) {
	dir := t.TempDir()

	pluginFile := filepath.Join(dir, "escape.js")
	code := `
		anna.registerTool({
			name: "escape_test",
			description: "Tries to read outside allowed dirs",
			parameters: {},
			execute: function(args) {
				return anna.readFile("../../etc/passwd");
			}
		});
	`
	if err := os.WriteFile(pluginFile, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := newTestRegistry()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	p, err := LoadJS(pluginFile, nil, reg, logger)
	if err != nil {
		t.Fatalf("LoadJS: %v", err)
	}
	defer func() { _ = p.Close() }()

	tool := reg.tools["escape_test"]
	_, err = tool.Execute(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for path escape attempt")
	}
}

func TestLoadJS_ConcurrentToolCalls(t *testing.T) {
	dir := t.TempDir()
	pluginFile := filepath.Join(dir, "counter.js")
	code := `
		var count = 0;
		anna.registerTool({
			name: "increment",
			description: "Increments counter",
			parameters: {},
			execute: function(args) {
				count++;
				return "" + count;
			}
		});
	`
	if err := os.WriteFile(pluginFile, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := newTestRegistry()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	p, err := LoadJS(pluginFile, nil, reg, logger)
	if err != nil {
		t.Fatalf("LoadJS: %v", err)
	}
	defer func() { _ = p.Close() }()

	tool := reg.tools["increment"]

	// Run 10 concurrent calls — mutex should serialize them.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := tool.Execute(context.Background(), nil)
			if err != nil {
				t.Errorf("concurrent Execute: %v", err)
			}
		}()
	}
	wg.Wait()

	// Final call should return "11" (10 previous + 1).
	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "11" {
		t.Errorf("result = %q, want %q", result, "11")
	}
}

func TestLoadJS_ClosedPlugin(t *testing.T) {
	dir := t.TempDir()
	pluginFile := filepath.Join(dir, "closeable.js")
	code := `
		anna.registerTool({
			name: "closeable_tool",
			description: "Tool on a closeable plugin",
			parameters: {},
			execute: function(args) {
				return "ok";
			}
		});
	`
	if err := os.WriteFile(pluginFile, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := newTestRegistry()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	p, err := LoadJS(pluginFile, nil, reg, logger)
	if err != nil {
		t.Fatalf("LoadJS: %v", err)
	}

	// Close the plugin, then try to execute.
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	tool := reg.tools["closeable_tool"]
	_, err = tool.Execute(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error after plugin close")
	}
}
