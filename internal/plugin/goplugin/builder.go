// Package goplugin implements Caddy-style compilation for Go plugins.
//
// It generates a temporary Go module that blank-imports each plugin package
// (triggering their init() registrations), then compiles a custom anna binary
// with all plugins statically linked.
package goplugin

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

const (
	annaModule    = "github.com/vaayne/anna"
	defaultOutput = "./anna-custom"
)

// Builder orchestrates Caddy-style compilation of a custom anna binary with
// Go plugins compiled in.
type Builder struct {
	plugins []string // absolute paths to plugin directories
	output  string   // output binary path
	logger  *slog.Logger
}

// NewBuilder creates a Builder. Each plugin path must be a local directory
// containing a go.mod. Output defaults to "./anna-custom" if empty.
func NewBuilder(plugins []string, output string, logger *slog.Logger) *Builder {
	if output == "" {
		output = defaultOutput
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Builder{
		plugins: plugins,
		output:  output,
		logger:  logger,
	}
}

// Build generates a temporary Go module and compiles a custom anna binary.
// The caller's context controls cancellation of the underlying go commands.
func (b *Builder) Build(ctx context.Context) error {
	if err := b.checkGoToolchain(ctx); err != nil {
		return err
	}

	modules, err := b.resolvePlugins()
	if err != nil {
		return err
	}

	annaRoot, err := b.findAnnaRoot()
	if err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp("", "anna-build-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	data := templateData{
		AnnaModule:    annaModule,
		AnnaLocalPath: annaRoot,
		Plugins:       modules,
	}

	// Copy cmd/anna source files into the temp dir so the build shares
	// the same package main as the real anna binary.
	cmdDir := filepath.Join(annaRoot, "cmd", "anna")
	if err := b.copySources(cmdDir, tmpDir); err != nil {
		return fmt.Errorf("copy cmd/anna sources: %w", err)
	}

	if err := b.writeFile(tmpDir, "plugins.go", pluginsGoTmpl, data); err != nil {
		return err
	}
	if err := b.writeFile(tmpDir, "go.mod", goModTmpl, data); err != nil {
		return err
	}

	b.logger.Info("running go mod tidy", "dir", tmpDir)
	if err := b.runGo(ctx, tmpDir, "mod", "tidy"); err != nil {
		return fmt.Errorf("go mod tidy: %w", err)
	}

	outputAbs, err := filepath.Abs(b.output)
	if err != nil {
		return fmt.Errorf("resolve output path: %w", err)
	}

	b.logger.Info("building custom binary", "output", outputAbs, "plugins", len(modules))
	if err := b.runGo(ctx, tmpDir, "build", "-o", outputAbs, "."); err != nil {
		return fmt.Errorf("go build: %w", err)
	}

	b.logger.Info("build complete", "binary", outputAbs)
	return nil
}

// checkGoToolchain verifies that the go toolchain is available.
func (b *Builder) checkGoToolchain(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "go", "version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go toolchain not found (is Go installed?): %w\n%s", err, out)
	}
	b.logger.Debug("go toolchain found", "version", strings.TrimSpace(string(out)))
	return nil
}

// resolvePlugins validates each plugin directory and reads its module path.
func (b *Builder) resolvePlugins() ([]pluginModule, error) {
	modules := make([]pluginModule, 0, len(b.plugins))
	for _, dir := range b.plugins {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("resolve plugin path %q: %w", dir, err)
		}

		modPath, err := readModulePath(filepath.Join(absDir, "go.mod"))
		if err != nil {
			return nil, fmt.Errorf("plugin %q: %w", absDir, err)
		}

		modules = append(modules, pluginModule{
			Module:    modPath,
			LocalPath: absDir,
		})
		b.logger.Debug("resolved plugin", "module", modPath, "path", absDir)
	}
	return modules, nil
}

// findAnnaRoot locates the anna project root by looking for go.mod in the
// current executable's directory hierarchy, falling back to the working
// directory hierarchy.
func (b *Builder) findAnnaRoot() (string, error) {
	// Try from executable location first.
	if exe, err := os.Executable(); err == nil {
		if root, ok := findGoModRoot(filepath.Dir(exe)); ok {
			return root, nil
		}
	}

	// Fall back to working directory.
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	if root, ok := findGoModRoot(cwd); ok {
		return root, nil
	}

	return "", fmt.Errorf("cannot find anna project root (no go.mod with module %s found)", annaModule)
}

// findGoModRoot walks up from dir looking for a go.mod containing the anna
// module path.
func findGoModRoot(dir string) (string, bool) {
	for {
		gomod := filepath.Join(dir, "go.mod")
		if modPath, err := readModulePath(gomod); err == nil && modPath == annaModule {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// readModulePath reads the module path from a go.mod file.
func readModulePath(gomodPath string) (string, error) {
	f, err := os.Open(gomodPath)
	if err != nil {
		return "", fmt.Errorf("open go.mod: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	return "", fmt.Errorf("no module directive found in %s", gomodPath)
}

// writeFile renders a template to a file in the given directory.
func (b *Builder) writeFile(dir, name string, tmpl *template.Template, data templateData) error {
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}

	if err := tmpl.Execute(f, data); err != nil {
		_ = f.Close()
		return fmt.Errorf("render %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	b.logger.Debug("wrote file", "path", path)
	return nil
}

// copySources copies all .go files (excluding test files) from srcDir to dstDir.
func (b *Builder) copySources(srcDir, dstDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("read source dir %s: %w", srcDir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(dstDir, name), data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
		b.logger.Debug("copied source file", "file", name)
	}
	return nil
}

// runGo executes a go command in the given directory.
func (b *Builder) runGo(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
