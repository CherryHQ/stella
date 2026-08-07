package delegate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"path"
	"testing"
	"testing/fstest"

	"github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/resources"
)

type runtimePresetFS struct {
	files     map[string][]byte
	entries   map[string][]sandbox.DirEntry
	listErr   map[string]error
	readErr   map[string]error
	readInfo  map[string]sandbox.FileInfo
	reader    io.ReadCloser
	nilReader bool
	listCalls []string
	closeErr  error
	closes    int
}

func (f *runtimePresetFS) Close() error { f.closes++; return f.closeErr }
func (f *runtimePresetFS) Read(_ context.Context, name string, _ sandbox.ReadOptions) (io.ReadCloser, sandbox.FileInfo, error) {
	if err := f.readErr[name]; err != nil {
		return nil, sandbox.FileInfo{}, err
	}
	data, ok := f.files[name]
	if !ok {
		return nil, sandbox.FileInfo{}, fs.ErrNotExist
	}
	info := sandbox.FileInfo{Name: path.Base(name), Size: int64(len(data)), Mode: 0o644}
	if configured, ok := f.readInfo[name]; ok {
		info = configured
	}
	if f.nilReader {
		return nil, info, nil
	}
	if f.reader != nil {
		return f.reader, info, nil
	}
	return io.NopCloser(bytes.NewReader(data)), info, nil
}

func (f *runtimePresetFS) Write(context.Context, string, io.Reader, sandbox.WriteOptions) error {
	return errors.New("unused")
}

func (f *runtimePresetFS) Upload(context.Context, string, io.Reader, sandbox.WriteOptions) error {
	return errors.New("unused")
}

func (f *runtimePresetFS) Stat(context.Context, string) (sandbox.FileInfo, error) {
	return sandbox.FileInfo{}, errors.New("unused")
}

func (f *runtimePresetFS) List(_ context.Context, name string) ([]sandbox.DirEntry, error) {
	f.listCalls = append(f.listCalls, name)
	if err := f.listErr[name]; err != nil {
		return nil, err
	}
	entries, ok := f.entries[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return entries, nil
}

func (f *runtimePresetFS) Mkdir(context.Context, string, fs.FileMode) error {
	return errors.New("unused")
}
func (f *runtimePresetFS) Remove(context.Context, string, bool) error   { return errors.New("unused") }
func (f *runtimePresetFS) Rename(context.Context, string, string) error { return errors.New("unused") }

type failingCloseReader struct {
	io.Reader
	err error
}

func (r failingCloseReader) Close() error { return r.err }

func preset(desc string) []byte            { return []byte("---\ndescription: " + desc + "\n---\nbody") }
func regular(name string) sandbox.DirEntry { return sandbox.DirEntry{Name: name, Mode: 0o644} }

func runtimeRegistry(t *testing.T) *resources.Registry {
	t.Helper()
	reg, err := resources.Load(fstest.MapFS{
		"delegates/a.md": &fstest.MapFile{Data: []byte("---\nname: shared\ndescription: builtin\ntools: []\ntimeout: 2m\nmodel: small\nmax_turns: 1\n---\nbuiltin body")},
		"delegates/b.md": &fstest.MapFile{Data: []byte("---\nname: omitted\ndescription: omitted\n---\nbody")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestLoadRuntimeDelegatePresetsTiersAndBuiltinMetadata(t *testing.T) {
	t.Parallel()
	fsys := &runtimePresetFS{files: map[string][]byte{}, entries: map[string][]sandbox.DirEntry{}}
	for _, tier := range []struct{ root, desc string }{{sandbox.PathUser, "user"}, {sandbox.PathWorkspace, "agent"}, {"/workspace/project", "project"}} {
		dir := path.Join(tier.root, ".agents", "delegates")
		fsys.entries[dir] = []sandbox.DirEntry{regular("shared.md"), regular(tier.desc + ".md")}
		fsys.files[path.Join(dir, "shared.md")] = preset(tier.desc)
		fsys.files[path.Join(dir, tier.desc+".md")] = preset(tier.desc)
	}
	presets, err := LoadRuntimeDelegatePresets(context.Background(), fsys, runtimeRegistry(t), RuntimePresetLoadConfig{HasPrincipal: true, ProjectRoot: "/workspace/project"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(presets), 5; got != want {
		t.Fatalf("len = %d, want %d", got, want)
	}
	var shared, omitted DelegatePreset
	for _, p := range presets {
		if p.Name == "shared" {
			shared = p
		}
		if p.Name == "omitted" {
			omitted = p
		}
	}
	if shared.Source != "project" || shared.Description != "project" {
		t.Fatalf("replacement = %+v", shared)
	}
	if presets[1].Name != "shared" { // builtin order is omitted, shared; replacement retains shared's slot.
		t.Fatalf("replacement moved slot: %+v", presets)
	}
	omitted = DelegatePreset{}
	for _, p := range presets {
		if p.Name == "omitted" {
			omitted = p
		}
	}
	if omitted.HasTools || omitted.FilePath != "builtin:omitted" {
		t.Fatalf("builtin omitted tools = %+v", omitted)
	}
	// The builtin shared was replaced, so inspect it in isolation for exact metadata.
	base, err := LoadRuntimeDelegatePresets(context.Background(), &runtimePresetFS{}, runtimeRegistry(t), RuntimePresetLoadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if !base[1].HasTools || len(base[1].Tools) != 0 || base[1].Timeout.String() != "2m0s" || base[1].Model != "small" {
		t.Fatalf("builtin metadata = %+v", base[1])
	}
}

func TestLoadRuntimeDelegatePresetsDeduplicatesProjectWorkspaceAndFailsClosed(t *testing.T) {
	t.Parallel()
	fsys := &runtimePresetFS{files: map[string][]byte{}, entries: map[string][]sandbox.DirEntry{path.Join(sandbox.PathWorkspace, ".agents", "delegates"): {regular("one.md")}}}
	fsys.files[path.Join(sandbox.PathWorkspace, ".agents", "delegates", "one.md")] = preset("one")
	presets, err := LoadRuntimeDelegatePresets(context.Background(), fsys, runtimeRegistry(t), RuntimePresetLoadConfig{ProjectRoot: sandbox.PathWorkspace})
	if err != nil || len(presets) != 3 {
		t.Fatalf("dedup presets=%d err=%v", len(presets), err)
	}
	fsys.listErr = map[string]error{path.Join(sandbox.PathWorkspace, ".agents", "delegates"): errors.New("transport")}
	if _, err := LoadRuntimeDelegatePresets(context.Background(), fsys, runtimeRegistry(t), RuntimePresetLoadConfig{}); err == nil {
		t.Fatal("transport error was ignored")
	}
}

func TestRuntimeBuiltinPresetExactMetadataAndBody(t *testing.T) {
	t.Parallel()
	registry, err := resources.Load(fstest.MapFS{
		"delegates/c.md": &fstest.MapFile{Data: []byte("---\nname: c\ndescription: c\ntools: [read, bash]\ntimeout: 3s\nmodel: model-c\n---\n\n body c \n")},
		"delegates/a.md": &fstest.MapFile{Data: []byte("---\nname: a\ndescription: a\n---\n\n body a \n")},
		"delegates/b.md": &fstest.MapFile{Data: []byte("---\nname: b\ndescription: b\ntools: []\n---\n\n body b \n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	presets, err := LoadRuntimeDelegatePresets(context.Background(), &runtimePresetFS{}, registry, RuntimePresetLoadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{presets[0].Name, presets[1].Name, presets[2].Name}; got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("deterministic order = %v", got)
	}
	if presets[0].HasTools || presets[0].Tools != nil || presets[0].System != "body a" {
		t.Fatalf("omitted tools/body = %+v", presets[0])
	}
	if !presets[1].HasTools || len(presets[1].Tools) != 0 || presets[1].System != "body b" {
		t.Fatalf("empty tools/body = %+v", presets[1])
	}
	if !presets[2].HasTools || len(presets[2].Tools) != 2 || presets[2].Timeout.String() != "3s" || presets[2].Model != "model-c" || presets[2].System != "body c" {
		t.Fatalf("metadata/body = %+v", presets[2])
	}
}

func TestRuntimeLoaderOptionalTiersAndMalformedFiles(t *testing.T) {
	t.Parallel()
	dir := path.Join(sandbox.PathWorkspace, ".agents", "delegates")
	fsys := &runtimePresetFS{files: map[string][]byte{
		path.Join(dir, "good.md"):        preset("good"),
		path.Join(dir, "bad-fm.md"):      []byte("bad"),
		path.Join(dir, "bad-desc.md"):    []byte("---\nname: bad\n---\nbody"),
		path.Join(dir, "bad-timeout.md"): []byte("---\ndescription: bad\ntimeout: no\n---\nbody"),
	}, entries: map[string][]sandbox.DirEntry{dir: {regular("good.md"), regular("bad-fm.md"), regular("bad-desc.md"), regular("bad-timeout.md")}}}
	presets, err := LoadRuntimeDelegatePresets(context.Background(), fsys, runtimeRegistry(t), RuntimePresetLoadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fsys.listCalls) != 1 || fsys.listCalls[0] != dir {
		t.Fatalf("unexpected tier lists: %v", fsys.listCalls)
	}
	var good bool
	for _, p := range presets {
		good = good || p.Name == "good" && p.Source == "agent"
	}
	if !good {
		t.Fatalf("valid sibling missing: %+v", presets)
	}
}

func TestRuntimeLoaderRejectsUnsafeEntriesAndReadFailures(t *testing.T) {
	t.Parallel()
	dir := path.Join(sandbox.PathWorkspace, ".agents", "delegates")
	registry := runtimeRegistry(t)
	for _, entry := range []sandbox.DirEntry{
		{Name: "../x.md", Mode: 0o644},
		{Name: "link.md", Mode: fs.ModeSymlink},
		{Name: "device.md", Mode: fs.ModeDevice},
		{Name: "lies.md", IsDir: true, Mode: 0o644},
	} {
		fsys := &runtimePresetFS{entries: map[string][]sandbox.DirEntry{dir: {entry}}}
		if _, err := LoadRuntimeDelegatePresets(context.Background(), fsys, registry, RuntimePresetLoadConfig{}); err == nil {
			t.Fatalf("accepted %#v", entry)
		}
	}
	file := path.Join(dir, "x.md")
	base := func() *runtimePresetFS {
		return &runtimePresetFS{files: map[string][]byte{file: preset("x")}, entries: map[string][]sandbox.DirEntry{dir: {regular("x.md")}}, readInfo: map[string]sandbox.FileInfo{}}
	}
	oversize := base()
	oversize.files[file] = bytes.Repeat([]byte("x"), int(maxRuntimeDelegateBytes)+1)
	oversize.readInfo[file] = sandbox.FileInfo{Size: int64(len(oversize.files[file])), Mode: 0o644}
	if _, err := LoadRuntimeDelegatePresets(context.Background(), oversize, registry, RuntimePresetLoadConfig{}); !errors.Is(err, sandbox.ErrReadLimit) {
		t.Fatalf("oversize err = %v", err)
	}
	mismatch := base()
	mismatch.readInfo[file] = sandbox.FileInfo{Size: 1, Mode: 0o644}
	if _, err := LoadRuntimeDelegatePresets(context.Background(), mismatch, registry, RuntimePresetLoadConfig{}); err == nil {
		t.Fatal("length mismatch accepted")
	}
	readFailure := base()
	readFailure.readErr = map[string]error{file: errors.New("read failed")}
	if _, err := LoadRuntimeDelegatePresets(context.Background(), readFailure, registry, RuntimePresetLoadConfig{}); err == nil {
		t.Fatal("read failure accepted")
	}
	closeFailure := base()
	closeErr := errors.New("reader close")
	closeFailure.reader = failingCloseReader{Reader: bytes.NewReader(closeFailure.files[file]), err: closeErr}
	if _, err := LoadRuntimeDelegatePresets(context.Background(), closeFailure, registry, RuntimePresetLoadConfig{}); !errors.Is(err, closeErr) {
		t.Fatalf("reader close error lost: %v", err)
	}
	nilReader := base()
	nilReader.nilReader = true
	if _, err := LoadRuntimeDelegatePresets(context.Background(), nilReader, registry, RuntimePresetLoadConfig{}); err == nil {
		t.Fatal("nil reader accepted")
	}
}
