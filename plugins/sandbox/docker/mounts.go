package docker

import (
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
	"github.com/CherryHQ/stella/plugins/sandbox/hostlayout"
)

// configureSessionMounts wires the immutable host layout for the active mode.
// It returns only mounts that are visible to this session's container.
func (f *dockerFactory) configureSessionMounts(opts *dockerclient.CreateOptions, layout hostlayout.Layout, tempDirHost string) ([]hostlayout.Mount, string, string, error) {
	if f.cfg.RuntimeMode == DockerSandboxModeVolume {
		return f.configureVolumeMounts(opts, layout, tempDirHost)
	}
	return f.configureBindMounts(opts, layout, tempDirHost)
}

func (f *dockerFactory) configureVolumeMounts(opts *dockerclient.CreateOptions, layout hostlayout.Layout, tempDirHost string) ([]hostlayout.Mount, string, string, error) {
	workspaceHost := layout.WorkspaceSource
	workspaceSubpath, ok := relativePathWithin(f.cfg.StellaHome, workspaceHost)
	if !ok {
		return nil, "", "", fmt.Errorf("docker session: workspace %q is not inside STELLA_HOME %q; cannot use volume mode", workspaceHost, f.cfg.StellaHome)
	}
	if workspaceSubpath == "." {
		return nil, "", "", fmt.Errorf("docker session: workspace must be a subdirectory of STELLA_HOME, not STELLA_HOME itself")
	}
	opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{HostPath: f.cfg.StellaHomeVolume, ContainerPath: workspaceMount, Type: dockerclient.MountTypeVolume, VolumeSubpath: filepath.ToSlash(workspaceSubpath)})
	mounted := []hostlayout.Mount{{Source: workspaceHost, Target: workspaceMount, Access: hostlayout.ReadWrite}}
	mountedUserDataHost := ""
	for _, m := range nonWorkspaceLayoutMounts(layout.Mounts) {
		if m.Access == hostlayout.ReadOnly && !dirExists(m.Source) {
			continue
		}
		subpath, ok := relativePathWithin(f.cfg.StellaHome, m.Source)
		if !ok || subpath == "." {
			logSkippedSandboxMount(DockerSandboxModeVolume, m.Source, "path is outside STELLA_HOME and cannot be mounted from the named volume")
			continue
		}
		opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{HostPath: f.cfg.StellaHomeVolume, ContainerPath: m.Target, ReadOnly: m.Access == hostlayout.ReadOnly, Type: dockerclient.MountTypeVolume, VolumeSubpath: filepath.ToSlash(subpath)})
		mounted = append(mounted, m)
		if m.Target == userDataMount {
			mountedUserDataHost = m.Source
		}
		if m.Access == hostlayout.ReadOnly {
			appendWorkspaceRelativeReadOnlyMount(opts, f.cfg.StellaHomeVolume, m.Source, workspaceHost, subpath, dockerclient.MountTypeVolume)
		}
	}
	mountedTempDirHost := ""
	if tempDirHost != "" {
		tmpSubpath, ok := relativePathWithin(f.cfg.StellaHome, tempDirHost)
		if !ok || tmpSubpath == "." {
			return nil, "", "", fmt.Errorf("docker session: temp directory %q is not a STELLA_HOME subdirectory in volume mode", tempDirHost)
		}
		opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{HostPath: f.cfg.StellaHomeVolume, ContainerPath: "/tmp", Type: dockerclient.MountTypeVolume, VolumeSubpath: filepath.ToSlash(tmpSubpath)})
		mountedTempDirHost = tempDirHost
	}
	opts.WorkspaceHost = ""
	return mounted, mountedTempDirHost, mountedUserDataHost, nil
}

func (f *dockerFactory) configureBindMounts(opts *dockerclient.CreateOptions, layout hostlayout.Layout, tempDirHost string) ([]hostlayout.Mount, string, string, error) {
	workspaceHost := layout.WorkspaceSource
	daemonWorkspaceHost, ok := f.cfg.daemonPath(workspaceHost)
	if !ok {
		return nil, "", "", fmt.Errorf("docker session: workspace %q is not under STELLA_HOME %q; cannot use bind-mount mode", workspaceHost, f.cfg.StellaHome)
	}
	opts.WorkspaceHost = daemonWorkspaceHost
	mounted := []hostlayout.Mount{{Source: workspaceHost, Target: workspaceMount, Access: hostlayout.ReadWrite}}
	mountedUserDataHost := ""
	for _, m := range nonWorkspaceLayoutMounts(layout.Mounts) {
		if m.Access == hostlayout.ReadOnly && !dirExists(m.Source) {
			continue
		}
		daemonPath, ok := f.cfg.daemonPath(m.Source)
		if !ok {
			logSkippedSandboxMount(f.cfg.RuntimeMode, m.Source, "path is not visible to the Docker daemon")
			continue
		}
		opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{HostPath: daemonPath, ContainerPath: m.Target, ReadOnly: m.Access == hostlayout.ReadOnly})
		mounted = append(mounted, m)
		if m.Target == userDataMount {
			mountedUserDataHost = m.Source
		}
		if m.Access == hostlayout.ReadOnly {
			appendWorkspaceRelativeReadOnlyMount(opts, daemonPath, m.Source, workspaceHost, "", dockerclient.MountTypeBind)
		}
	}
	mountedTempDirHost := ""
	if tempDirHost != "" {
		if daemonPath, ok := f.cfg.daemonPath(tempDirHost); ok {
			opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{HostPath: daemonPath, ContainerPath: "/tmp"})
			mountedTempDirHost = tempDirHost
		} else {
			logSkippedSandboxMount(f.cfg.RuntimeMode, tempDirHost, "path is not visible to the Docker daemon")
		}
	}
	return mounted, mountedTempDirHost, mountedUserDataHost, nil
}

// applyDockerFilesystemEnv renders process-view roots that translate through
// the actual mount table. Docker never publishes an unmounted /tmp.
func applyDockerFilesystemEnv(env map[string]string, workspaceHost, userDataHost, tempDirHost string) error {
	return sandboxpkg.ApplyFilesystemEnv(env, sandboxpkg.FilesystemView{
		Home:          workspaceHost,
		SharedDataDir: userDataHost,
		TempDir:       tempDirHost,
	})
}

func logSkippedSandboxMount(mode DockerSandboxMode, source, reason string) {
	slog.Warn("docker backend: skipping sandbox mount", "component", "runner_sandbox", "mode", mode, "path", source, "reason", reason)
}

func dirExists(path string) bool { _, err := os.Stat(path); return err == nil }

func relativePathWithin(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

func cleanContainerPath(containerPath string) string {
	return path.Clean(strings.ReplaceAll(containerPath, "\\", "/"))
}

func dockerMountProvidedByImage(m hostlayout.Mount) bool {
	if !strings.HasPrefix(m.Target, stellaHomeMount+"/") {
		return false
	}
	rel, ok := sandboxpkg.POSIXPathRelative(stellaHomeMount, m.Target)
	if !ok {
		return false
	}
	_, ok = dockerImageProvidedStellaDirs[rel]
	return ok
}

func nonWorkspaceLayoutMounts(mounts []hostlayout.Mount) []hostlayout.Mount {
	out := make([]hostlayout.Mount, 0, len(mounts))
	for _, m := range mounts {
		if m.Target != workspaceMount && !dockerMountProvidedByImage(m) {
			out = append(out, m)
		}
	}
	return out
}

func appendWorkspaceRelativeReadOnlyMount(opts *dockerclient.CreateOptions, source, hostPath, workspaceHost, volumeSubpath string, mountType dockerclient.MountType) {
	rel, ok := relativePathWithin(workspaceHost, hostPath)
	if !ok || rel == "." {
		return
	}
	opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{HostPath: source, ContainerPath: path.Join(workspaceMount, strings.ReplaceAll(rel, "\\", "/")), ReadOnly: true, Type: mountType, VolumeSubpath: filepath.ToSlash(volumeSubpath)})
}

var dockerImageProvidedStellaDirs = map[string]struct{}{"bin": {}, ".mise-tools": {}, "skills/builtin": {}}

type writableMount struct{ Host, Container string }

func writableToolTrees(mounts []hostlayout.Mount) []writableMount {
	out := []writableMount{}
	for _, m := range mounts {
		if m.Access == hostlayout.ReadWrite && m.Target != workspaceMount && m.Target != userDataMount {
			out = append(out, writableMount{Host: m.Source, Container: m.Target})
		}
	}
	return out
}

type mountTableOptions struct {
	WorkspaceHost, WorkspaceMount string
	Mounts                        []hostlayout.Mount
	TempHost                      string
}

// buildMountTable uses process-view sources, never daemon-translated sources.
func buildMountTable(opts mountTableOptions) []dockerclient.Mount {
	mounts := make([]dockerclient.Mount, 0, len(opts.Mounts)+1)
	for _, m := range opts.Mounts {
		mounts = append(mounts, dockerclient.Mount{HostPath: m.Source, ContainerPath: m.Target, ReadOnly: m.Access == hostlayout.ReadOnly})
	}
	if len(mounts) == 0 {
		mounts = append(mounts, dockerclient.Mount{HostPath: opts.WorkspaceHost, ContainerPath: cleanContainerPath(opts.WorkspaceMount)})
	}
	if opts.TempHost != "" {
		mounts = append(mounts, dockerclient.Mount{HostPath: opts.TempHost, ContainerPath: "/tmp"})
	}
	return mounts
}
