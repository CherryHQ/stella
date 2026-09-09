package docker

import (
	"context"
	"strings"
	"testing"

	mobyclient "github.com/moby/moby/client"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
	"github.com/CherryHQ/stella/plugins/sandbox/internal/sessionfs"
)

type prepareEnvRecordingAPI struct {
	noopAPI
	envs   [][]string
	unsets [][]string
}

func (a *prepareEnvRecordingAPI) ExecCreate(_ context.Context, _ string, opts mobyclient.ExecCreateOptions) (mobyclient.ExecCreateResult, error) {
	a.envs = append(a.envs, append([]string(nil), opts.Env...))
	var unset []string
	for _, entry := range opts.Env {
		if !strings.Contains(entry, "=") {
			unset = append(unset, entry)
		}
	}
	a.unsets = append(a.unsets, unset)
	return mobyclient.ExecCreateResult{ID: "exec"}, nil
}

func TestPreparePluginBinariesPublishesIntoRetainedContainerProjection(t *testing.T) {
	resetToolCacheStateForTest()
	defer resetToolCacheStateForTest()
	installSelectionToolCacheFn = func(_ context.Context, _ *dockerclient.Client, _ Config, _ string, _ string, _ string, cache *selectionToolCache) (*selectionToolCache, error) {
		return cache, nil
	}
	session := &dockerSession{
		client:           dockerclient.NewWithAPI(noopAPI{}),
		containerID:      "retained-container",
		selectionImageID: "sha256:retained-image",
		selectionConfig:  Config{Runtime: "runsc", StableProjectionID: "test-session"},
		toolBinPaths:     []string{"/old/selection"},
		coreToolBinPaths: []string{"/core/bin"},
	}
	specs := []pkgplugins.PluginBinarySpec{{
		PluginResourceIdentity: pkgplugins.PluginResourceIdentity{PluginID: "plugin/demo", ConfigID: "config/demo", Scope: "user", Revision: 1},
		PackageDigest:          "sha256:package",
		Name:                   "demo",
		Tool:                   "uv",
		Version:                "1",
	}}
	preparation, paths, err := session.PreparePluginBinaries(t.Context(), specs)
	if err != nil {
		t.Fatalf("PreparePluginBinaries: %v", err)
	}
	if len(paths) != 1 || !strings.HasPrefix(paths[0], containerStableSelectionRoot+"/packages/") || !strings.HasSuffix(paths[0], "/bin") {
		t.Fatalf("optional package paths = %v, want one stable package bin path", paths)
	}
	if got := preparation.Status("plugin/demo"); !got.Ready {
		t.Fatalf("preparation status = %+v, want ready", got)
	}
	if len(preparation.Binaries) != 1 || preparation.Binaries[0].Backend != "docker" {
		t.Fatalf("binary evidence = %+v, want one docker record", preparation.Binaries)
	}
	if session.containerID != "retained-container" {
		t.Fatal("preparation replaced the retained container identity")
	}
	if len(session.toolBinPaths) != 1 || session.toolBinPaths[0] != "/old/selection" {
		t.Fatalf("tool paths mutated = %v", session.toolBinPaths)
	}
	if len(session.coreToolBinPaths) != 1 || session.coreToolBinPaths[0] != "/core/bin" {
		t.Fatalf("core tool paths mutated = %v", session.coreToolBinPaths)
	}
}

func TestPreparePluginBinariesEmptySelectionReturnsNoPaths(t *testing.T) {
	session := &dockerSession{closed: true}
	preparation, paths, err := session.PreparePluginBinaries(t.Context(), nil)
	if err != nil {
		t.Fatalf("empty PreparePluginBinaries: %v", err)
	}
	if preparation.Packages != nil || preparation.Binaries != nil || paths != nil {
		t.Fatalf("empty preparation = %+v, paths = %v, want nil result and paths", preparation, paths)
	}
}

func TestPreparePluginBinariesRequiresStableProjection(t *testing.T) {
	session := &dockerSession{
		client:           dockerclient.NewWithAPI(noopAPI{}),
		selectionImageID: "sha256:retained-image",
	}
	_, paths, err := session.PreparePluginBinaries(t.Context(), []pkgplugins.PluginBinarySpec{{Name: "demo", Tool: "uv", Version: "1"}})
	if err == nil || !strings.Contains(err.Error(), "stable selection projection") {
		t.Fatalf("error = %v, want stable projection error", err)
	}
	if paths != nil {
		t.Fatalf("paths = %v, want nil on failure", paths)
	}
}

func TestDockerExecAndStartProcessReplaceCurrentSelection(t *testing.T) {
	api := &prepareEnvRecordingAPI{}
	root := t.TempDir()
	resolver, err := sessionfs.NewResolver("/workspace", []sessionfs.Mount{{HostPath: root, SandboxPath: "/workspace"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resolver.Close() })
	session := &dockerSession{
		client:      dockerclient.NewWithAPI(api),
		containerID: "retained-container",
		policy: sandboxpkg.Policy{
			Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: "/workspace"},
			Env:        map[string]string{sandboxpkg.EnvNativeSelectionDir: "/old/selection"},
		},
		mountTable:   []dockerclient.Mount{{HostPath: root, ContainerPath: "/workspace"}},
		envPathMaps:  []envPathMap{{HostPrefix: "/host/stella", ContainerPrefix: "/opt/stella"}},
		toolBinPaths: []string{"/old/selection"}, coreToolBinPaths: []string{"/core/bin"},
		resolver: resolver,
	}
	session.host = &dockerHost{session: session}
	current := map[string]string{
		sandboxpkg.EnvNativeSelectionDir:     "/host/stella/current/system",
		sandboxpkg.EnvUserNativeSelectionDir: "/host/stella/current/user",
		"PATH":                               "/host/path:/usr/bin",
	}
	if _, err := session.Exec(t.Context(), "true", sandboxpkg.ExecOptions{Env: current, EnvMode: sandboxpkg.EnvReplace}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if _, err := session.Exec(t.Context(), "true", sandboxpkg.ExecOptions{Env: map[string]string{"PATH": "/host/path:/usr/bin"}, EnvMode: sandboxpkg.EnvReplace}); err != nil {
		t.Fatalf("revoked Exec: %v", err)
	}
	process, err := session.StartProcess(t.Context(), sandboxpkg.ProcessRequest{Path: "true", Env: current, EnvMode: sandboxpkg.EnvReplace})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	_ = process.Close()
	if len(api.envs) != 3 {
		t.Fatalf("recorded envs = %d, want Exec, revoked Exec, StartProcess", len(api.envs))
	}
	wantCurrent := "/opt/stella/current/user:/opt/stella/current/system:/core/bin:" + containerDefaultPATH
	if got := recordedEnvValue(api.envs[0], "PATH"); got != wantCurrent {
		t.Fatalf("Exec PATH = %q, want %q", got, wantCurrent)
	}
	if got := recordedEnvValue(api.envs[1], "PATH"); got != "/core/bin:"+containerDefaultPATH {
		t.Fatalf("revoked Exec PATH = %q, want core-only", got)
	}
	if got := recordedEnvValue(api.envs[2], "PATH"); got != wantCurrent {
		t.Fatalf("StartProcess PATH = %q, want %q", got, wantCurrent)
	}
	for _, env := range api.envs {
		path := recordedEnvValue(env, "PATH")
		if strings.Contains(path, "/host/") || strings.Contains(path, "/old/") {
			t.Fatalf("host or stale path leaked into recorded PATH: %q", path)
		}
	}
}

func TestDockerEnvReplaceUnsetsCreationPolicyKeys(t *testing.T) {
	api := &prepareEnvRecordingAPI{}
	root := t.TempDir()
	resolver, err := sessionfs.NewResolver("/workspace", []sessionfs.Mount{{HostPath: root, SandboxPath: "/workspace"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resolver.Close() })
	session := &dockerSession{
		client:      dockerclient.NewWithAPI(api),
		containerID: "retained-container",
		policy: sandboxpkg.Policy{
			Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: "/workspace"},
			Env: map[string]string{
				"OLD_TOKEN": "synthetic-secret",
				"KEEP":      "old",
			},
		},
		creationEnvKeys: []string{"KEEP", "OLD_TOKEN"},
		mountTable:      []dockerclient.Mount{{HostPath: root, ContainerPath: "/workspace"}},
		resolver:        resolver,
	}
	session.host = &dockerHost{session: session}
	if _, err := session.Exec(t.Context(), "true", sandboxpkg.ExecOptions{
		Env:     map[string]string{"KEEP": "new"},
		EnvMode: sandboxpkg.EnvReplace,
	}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if len(api.unsets) != 1 || len(api.unsets[0]) != 1 || api.unsets[0][0] != "OLD_TOKEN" {
		t.Fatalf("Exec unset env = %v, want [OLD_TOKEN]", api.unsets)
	}
	process, err := session.StartProcess(t.Context(), sandboxpkg.ProcessRequest{
		Path:    "true",
		Env:     map[string]string{"KEEP": "new"},
		EnvMode: sandboxpkg.EnvReplace,
	})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	_ = process.Close()
	if len(api.unsets) != 2 || len(api.unsets[1]) != 1 || api.unsets[1][0] != "OLD_TOKEN" {
		t.Fatalf("StartProcess unset env = %v, want [OLD_TOKEN]", api.unsets)
	}
}

func recordedEnvValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if after, ok := strings.CutPrefix(entry, prefix); ok {
			return after
		}
	}
	return ""
}
