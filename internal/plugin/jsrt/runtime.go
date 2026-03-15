package jsrt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/fastschema/qjs"
	pluginapi "github.com/vaayne/anna/pkg/plugin"
)

// JSPlugin wraps a single JS extension loaded via QJS.
// All JS calls are serialized via mu since QJS runtimes are not goroutine-safe.
type JSPlugin struct {
	name    string
	mu      sync.Mutex
	runtime *qjs.Runtime
}

func (p *JSPlugin) Name() string                   { return p.name }
func (p *JSPlugin) Init(_ pluginapi.Context) error { return nil }

func (p *JSPlugin) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.runtime != nil {
		p.runtime.Close()
		p.runtime = nil
	}
	return nil
}

// ToolHookRegistrar is the subset of Registry that LoadJS needs.
type ToolHookRegistrar interface {
	RegisterTool(pluginapi.Tool) error
	RegisterHook(pluginapi.EventKind, pluginapi.HookFunc)
}

// LoadJS loads a JS extension from the given path, registers its tools and hooks
// into the registry, and returns a JSPlugin that satisfies pluginapi.Plugin.
func LoadJS(path string, cfg map[string]any, registry ToolHookRegistrar, logger *slog.Logger) (*JSPlugin, error) {
	code, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plugin file: %w", err)
	}

	pluginName := pluginNameFromPath(path)
	pluginDir := filepath.Dir(path)
	plog := logger.With("plugin", pluginName)

	rt, err := qjs.New()
	if err != nil {
		return nil, fmt.Errorf("create qjs runtime: %w", err)
	}

	p := &JSPlugin{
		name:    pluginName,
		runtime: rt,
	}

	ctx := rt.Context()

	// Collectors for tools and hooks registered during plugin init.
	type toolEntry struct {
		tool      pluginapi.Tool
		executeFn *qjs.Value
	}
	var toolEntries []toolEntry

	type hookEntry struct {
		event   pluginapi.EventKind
		handler *qjs.Value
	}
	var hookEntries []hookEntry

	// Build the anna host object.
	anna := ctx.NewObject()

	// anna.config
	configVal, err := goToJSValue(ctx, cfg)
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("convert config to JS: %w", err)
	}
	anna.SetPropertyStr("config", configVal)

	// Host APIs: log, readFile, writeFile, fetch.
	ha := &hostAPI{
		logger:    plog,
		pluginDir: pluginDir,
	}
	registerHostAPIs(ctx, anna, ha)

	// anna.registerTool(def)
	anna.SetPropertyStr("registerTool", ctx.Function(func(this *qjs.This) (*qjs.Value, error) {
		t, execFn, err := parseToolDef(this)
		if err != nil {
			return nil, err
		}
		toolEntries = append(toolEntries, toolEntry{tool: t, executeFn: execFn})
		return this.Context().NewBool(true), nil
	}))

	// anna.on(event, handler)
	anna.SetPropertyStr("on", ctx.Function(func(this *qjs.This) (*qjs.Value, error) {
		args := this.Args()
		if len(args) < 2 {
			return nil, errors.New("anna.on requires (event, handler)")
		}
		if !args[1].IsFunction() {
			return nil, errors.New("anna.on second argument must be a function")
		}
		hookEntries = append(hookEntries, hookEntry{
			event:   pluginapi.EventKind(args[0].String()),
			handler: args[1].Clone(),
		})
		return this.Context().NewBool(true), nil
	}))

	// Execute the plugin code. The plugin file should be a function body that
	// receives `anna` as its argument. We wrap it in an IIFE.
	wrappedCode := fmt.Sprintf(
		"(function(anna) {\n%s\n})(__anna_host);\n",
		string(code),
	)
	ctx.Global().SetPropertyStr("__anna_host", anna)

	_, err = ctx.Eval(filepath.Base(path), qjs.Code(wrappedCode))
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("eval plugin %s: %w", pluginName, err)
	}

	// Register collected tools.
	for _, te := range toolEntries {
		t := te.tool
		execFn := te.executeFn
		t.Execute = func(_ context.Context, args map[string]any) (string, error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.runtime == nil {
				return "", errors.New("plugin is closed")
			}
			c := rt.Context()
			argsVal, err := goToJSValue(c, args)
			if err != nil {
				return "", fmt.Errorf("convert args: %w", err)
			}
			result, err := c.Invoke(execFn, c.NewNull(), argsVal)
			if err != nil {
				return "", fmt.Errorf("tool execute: %w", err)
			}
			defer result.Free()
			return result.String(), nil
		}
		if err := registry.RegisterTool(t); err != nil {
			plog.Warn("failed to register tool", "tool", t.Name, "error", err)
		}
	}

	// Register collected hooks.
	for _, he := range hookEntries {
		handler := he.handler
		fn := func(_ context.Context, event any) error {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.runtime == nil {
				return nil
			}
			c := rt.Context()
			eventVal, err := goToJSValue(c, event)
			if err != nil {
				plog.Warn("convert hook event failed", "error", err)
				return nil
			}
			result, err := c.Invoke(handler, c.NewNull(), eventVal)
			if err != nil {
				plog.Warn("hook handler error", "error", err)
				return nil
			}
			defer result.Free()
			// Hook cancellation: returning a non-empty string blocks the action.
			if result.IsString() {
				reason := result.String()
				if reason != "" && reason != "undefined" {
					return errors.New(reason)
				}
			}
			return nil
		}
		registry.RegisterHook(he.event, fn)
	}

	plog.Info("JS plugin loaded", "tools", len(toolEntries), "hooks", len(hookEntries))
	return p, nil
}

// parseToolDef extracts tool metadata from the JS object argument.
func parseToolDef(this *qjs.This) (pluginapi.Tool, *qjs.Value, error) {
	args := this.Args()
	if len(args) < 1 {
		return pluginapi.Tool{}, nil, errors.New("registerTool requires a tool definition object")
	}
	def := args[0]

	name := def.GetPropertyStr("name")
	if name.IsUndefined() || name.String() == "" {
		return pluginapi.Tool{}, nil, errors.New("tool name is required")
	}

	desc := def.GetPropertyStr("description")
	executeFn := def.GetPropertyStr("execute")
	if executeFn.IsUndefined() || !executeFn.IsFunction() {
		return pluginapi.Tool{}, nil, fmt.Errorf("tool %q requires an execute function", name.String())
	}

	// Parse parameters/inputSchema.
	var inputSchema map[string]any
	params := def.GetPropertyStr("parameters")
	if !params.IsUndefined() && !params.IsNull() {
		ctx := this.Context()
		schema, err := jsValueToGoMap(ctx, params)
		if err == nil {
			inputSchema = schema
		}
	}

	t := pluginapi.Tool{
		Name:        name.String(),
		Description: desc.String(),
		InputSchema: inputSchema,
	}

	return t, executeFn.Clone(), nil
}

func pluginNameFromPath(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	return base[:len(base)-len(ext)]
}
