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
)

// configureSessionMounts wires the generic mount plan for the active mode and
// returns the mounted process-view mounts, the mounted temp-dir host, and the
// mounted user-data host ("" when /user could not be mounted, so callers don't
// wire a /user that isn't really there).
func (f *dockerFactory) configureSessionMounts(opts *dockerclient.CreateOptions, policy sandboxpkg.Policy, workspaceHost, userDataHost, tempDirHost string) ([]sandboxpkg.Mount, string, string, error) {
	if len(policy.Filesystem.Mounts) == 0 {
		policy.Filesystem.Mounts = append(policy.Filesystem.Mounts, sandboxpkg.Mount{HostPath: workspaceHost, SandboxPath: workspaceMount, Access: sandboxpkg.MountReadWrite})
	}
	if userDataHost != "" && hostPathForSandboxMount(policy.Filesystem.Mounts, userDataMount) == "" {
		policy.Filesystem.Mounts = append(policy.Filesystem.Mounts, sandboxpkg.Mount{HostPath: userDataHost, SandboxPath: userDataMount, Access: sandboxpkg.MountReadWrite})
	}
	if f.cfg.RuntimeMode == DockerSandboxModeVolume {
		mounted, mountedTempDirHost, mountedUserDataHost, err := f.configureVolumeMounts(opts, policy, workspaceHost, userDataHost, tempDirHost)
		return mounted, mountedTempDirHost, mountedUserDataHost, err
	}
	return f.configureBindMounts(opts, policy, workspaceHost, userDataHost, tempDirHost)
}

func (f *dockerFactory) configureVolumeMounts(opts *dockerclient.CreateOptions, policy sandboxpkg.Policy, workspaceHost, userDataHost, tempDirHost string) ([]sandboxpkg.Mount, string, string, error) {
	workspaceSubpath, ok := relativePathWithin(f.cfg.StellaHome, workspaceHost)
	if !ok {
		return nil, "", "", fmt.Errorf("docker session: workspace %q is not inside STELLA_HOME %q; cannot use volume mode", workspaceHost, f.cfg.StellaHome)
	}
	if workspaceSubpath == "." {
		return nil, "", "", fmt.Errorf("docker session: workspace must be a subdirectory of STELLA_HOME, not STELLA_HOME itself")
	}
	opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
		HostPath:      f.cfg.StellaHomeVolume,
		ContainerPath: workspaceMount,
		ReadOnly:      false,
		Type:          dockerclient.MountTypeVolume,
		VolumeSubpath: filepath.ToSlash(workspaceSubpath),
	})
	mounted := []sandboxpkg.Mount{{HostPath: workspaceHost, SandboxPath: workspaceMount, Access: sandboxpkg.MountReadWrite}}
	mountedUserDataHost := ""
	for _, m := range nonWorkspacePolicyMounts(policy.Filesystem.Mounts) {
		if m.Access == sandboxpkg.MountReadOnly && !dirExists(m.HostPath) {
			continue
		}
		subpath, ok := relativePathWithin(f.cfg.StellaHome, m.HostPath)
		if !ok || subpath == "." {
			logSkippedSandboxMount(DockerSandboxModeVolume, m.HostPath, "path is outside STELLA_HOME and cannot be mounted from the named volume")
			continue
		}
		opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
			HostPath:      f.cfg.StellaHomeVolume,
			ContainerPath: m.SandboxPath,
			ReadOnly:      m.Access == sandboxpkg.MountReadOnly,
			Type:          dockerclient.MountTypeVolume,
			VolumeSubpath: filepath.ToSlash(subpath),
		})
		mounted = append(mounted, m)
		if m.SandboxPath == userDataMount {
			mountedUserDataHost = m.HostPath
		}
		if m.Access == sandboxpkg.MountReadOnly {
			appendWorkspaceRelativeReadOnlyMount(opts, f.cfg.StellaHomeVolume, m.HostPath, workspaceHost, subpath, dockerclient.MountTypeVolume)
		}
	}
	mountedTempDirHost := ""
	if tempDirHost != "" {
		tmpSubpath, ok := relativePathWithin(f.cfg.StellaHome, tempDirHost)
		if !ok || tmpSubpath == "." {
			return nil, "", "", fmt.Errorf("docker session: temp directory %q is not a STELLA_HOME subdirectory in volume mode", tempDirHost)
		}
		opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
			HostPath:      f.cfg.StellaHomeVolume,
			ContainerPath: "/tmp",
			Type:          dockerclient.MountTypeVolume,
			VolumeSubpath: filepath.ToSlash(tmpSubpath),
		})
		mountedTempDirHost = tempDirHost
	}
	opts.WorkspaceHost = ""
	return mounted, mountedTempDirHost, mountedUserDataHost, nil
}

func (f *dockerFactory) configureBindMounts(opts *dockerclient.CreateOptions, policy sandboxpkg.Policy, workspaceHost, userDataHost, tempDirHost string) ([]sandboxpkg.Mount, string, string, error) {
	daemonWorkspaceHost, ok := f.cfg.daemonPath(workspaceHost)
	if !ok {
		return nil, "", "", fmt.Errorf("docker session: workspace %q is not under STELLA_HOME %q; cannot use bind-mount mode", workspaceHost, f.cfg.StellaHome)
	}
	opts.WorkspaceHost = daemonWorkspaceHost
	mounted := []sandboxpkg.Mount{{HostPath: workspaceHost, SandboxPath: workspaceMount, Access: sandboxpkg.MountReadWrite}}
	mountedUserDataHost := ""
	for _, m := range nonWorkspacePolicyMounts(policy.Filesystem.Mounts) {
		if m.Access == sandboxpkg.MountReadOnly && !dirExists(m.HostPath) {
			continue
		}
		daemonPath, ok := f.cfg.daemonPath(m.HostPath)
		if !ok {
			logSkippedSandboxMount(f.cfg.RuntimeMode, m.HostPath, "path is not visible to the Docker daemon")
			continue
		}
		opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
			HostPath:      daemonPath,
			ContainerPath: m.SandboxPath,
			ReadOnly:      m.Access == sandboxpkg.MountReadOnly,
		})
		mounted = append(mounted, m)
		if m.SandboxPath == userDataMount {
			mountedUserDataHost = m.HostPath
		}
		if m.Access == sandboxpkg.MountReadOnly {
			appendWorkspaceRelativeReadOnlyMount(opts, daemonPath, m.HostPath, workspaceHost, "", dockerclient.MountTypeBind)
		}
	}
	mountedTempDirHost := ""
	if tempDirHost != "" {
		if daemonPath, ok := f.cfg.daemonPath(tempDirHost); ok {
			opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
				HostPath:      daemonPath,
				ContainerPath: "/tmp",
			})
			mountedTempDirHost = tempDirHost
		} else {
			logSkippedSandboxMount(f.cfg.RuntimeMode, tempDirHost, "path is not visible to the Docker daemon")
		}
	}
	return mounted, mountedTempDirHost, mountedUserDataHost, nil
}

func logSkippedSandboxMount(mode DockerSandboxMode, path, reason string) {
	slog.Warn("docker backend: skipping sandbox mount",
		"component", "runner_sandbox",
		"mode", mode,
		"path", path,
		"reason", reason,
	)
}

// dirExists reports whether path exists on the host (file or directory). Used to
// skip optional mounts whose source is absent for this session.
func dirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func relativePathWithin(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// cleanContainerPath canonicalizes a value at a Linux-container path boundary.
func cleanContainerPath(containerPath string) string {
	return path.Clean(strings.ReplaceAll(containerPath, "\\", "/"))
}

// normalizeDockerPolicyMounts canonicalizes the container-space half of the
// policy exactly once at CreateSession's boundary. Host paths retain native
// semantics and must not be normalized as POSIX paths.
func normalizeDockerPolicyMounts(mounts []sandboxpkg.Mount) []sandboxpkg.Mount {
	out := append([]sandboxpkg.Mount(nil), mounts...)
	for i := range out {
		out[i].SandboxPath = cleanContainerPath(out[i].SandboxPath)
	}
	return out
}

func hostPathForSandboxMount(mounts []sandboxpkg.Mount, sandboxPath string) string {
	for _, m := range mounts {
		if m.SandboxPath == sandboxPath {
			return m.HostPath
		}
	}
	return ""
}

// applyDockerFilesystemEnv renders process-view roots that translate through
// the actual mount table. tempDirHost is required: Docker never publishes an
// unmounted /tmp because host-side file tools must resolve the same bytes.
func applyDockerFilesystemEnv(env map[string]string, workspaceHost, userDataHost, tempDirHost string) error {
	return sandboxpkg.ApplyFilesystemEnv(env, sandboxpkg.FilesystemView{
		Home:          workspaceHost,
		SharedDataDir: userDataHost,
		TempDir:       tempDirHost,
	})
}

func dockerMountProvidedByImage(m sandboxpkg.Mount) bool {
	containerPath := m.SandboxPath
	if !strings.HasPrefix(containerPath, stellaHomeMount+"/") {
		return false
	}
	rel, ok := sandboxpkg.POSIXPathRelative(stellaHomeMount, containerPath)
	if !ok {
		return false
	}
	_, ok = dockerImageProvidedStellaDirs[rel]
	return ok
}

func nonWorkspacePolicyMounts(mounts []sandboxpkg.Mount) []sandboxpkg.Mount {
	out := make([]sandboxpkg.Mount, 0, len(mounts))
	for _, m := range mounts {
		if m.SandboxPath == workspaceMount || dockerMountProvidedByImage(m) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func appendWorkspaceRelativeReadOnlyMount(opts *dockerclient.CreateOptions, source, hostPath, workspaceHost, volumeSubpath string, mountType dockerclient.MountType) {
	rel, ok := relativePathWithin(workspaceHost, hostPath)
	if !ok || rel == "." {
		return
	}
	opts.ExtraMounts = append(opts.ExtraMounts, dockerclient.Mount{
		HostPath:      source,
		ContainerPath: path.Join(workspaceMount, strings.ReplaceAll(rel, "\\", "/")),
		ReadOnly:      true,
		Type:          mountType,
		VolumeSubpath: filepath.ToSlash(volumeSubpath),
	})
}

// dockerImageProvidedStellaDirs are STELLA_HOME subdirs the sandbox image bakes
// itself, built for the container's linux platform: the mise binary (bin) and the
// shared system mise tree (.mise-tools). They must NOT be mounted from the host —
// whose binaries may be a different platform — so the /opt/stella image versions
// win and the per-user relative symlinks resolve against a runnable system tree.
var dockerImageProvidedStellaDirs = map[string]struct{}{
	"bin":         {},
	".mise-tools": {},
}

// writableMount is a per-user writable tree mounted into the container, recording
// both ends so the caller can register it in the mount table and derive PATH.
type writableMount struct {
	Host      string
	Container string
}

func writableToolTrees(mounts []sandboxpkg.Mount) []writableMount {
	out := []writableMount{}
	for _, m := range mounts {
		if m.Access == sandboxpkg.MountReadWrite && m.SandboxPath != workspaceMount && m.SandboxPath != userDataMount {
			out = append(out, writableMount{Host: m.HostPath, Container: m.SandboxPath})
		}
	}
	return out
}

type mountTableOptions struct {
	WorkspaceHost  string
	WorkspaceMount string
	Mounts         []sandboxpkg.Mount
	TempHost       string
}

// buildMountTable returns the process-view bind mount set that path resolution
// should consult. Host paths here are intentionally not daemon-translated: file
// tools run in the Stella process namespace, not the Docker daemon namespace.
func buildMountTable(opts mountTableOptions) []dockerclient.Mount {
	mounts := make([]dockerclient.Mount, 0, len(opts.Mounts)+1)
	if len(opts.Mounts) == 0 {
		mounts = append(mounts, dockerclient.Mount{HostPath: opts.WorkspaceHost, ContainerPath: cleanContainerPath(opts.WorkspaceMount)})
	} else {
		for _, m := range opts.Mounts {
			mounts = append(mounts, dockerclient.Mount{
				HostPath:      m.HostPath,
				ContainerPath: m.SandboxPath,
				ReadOnly:      m.Access == sandboxpkg.MountReadOnly,
			})
		}
	}
	if opts.TempHost != "" {
		mounts = append(mounts, dockerclient.Mount{HostPath: opts.TempHost, ContainerPath: "/tmp"})
	}
	return mounts
}
