package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"

	sandbox "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/internal/sessionfs"
)

const finalizer = "stella.cherryhq.io/execution-fence"

type factory struct {
	client         *Client
	sources        map[string]string
	generation     int64
	executorBootID string
}

func (c *Client) Factory(sources map[string]string) *factory {
	return &factory{client: c, sources: maps.Clone(sources)}
}

// FactoryWithGeneration binds resources created by this factory to one
// durable SessionSandbox generation. The labels are omitted for legacy callers
// that do not participate in generation ownership, preserving their strict
// startup cleanup path.
func (c *Client) FactoryWithGeneration(sources map[string]string, generation int64, executorBootID string) *factory {
	return &factory{client: c, sources: maps.Clone(sources), generation: generation, executorBootID: executorBootID}
}
func (f *factory) Name() string                     { return "kubernetes" }
func (f *factory) Available() bool                  { return f.client != nil }
func (f *factory) Supported(p sandbox.Policy) error { return p.Validate() }

func subPath(home, source string) (string, error) {
	home = filepath.Clean(home)
	rel, err := filepath.Rel(home, source)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", errors.New("kubernetes: mount must be a strict PVC subdirectory")
	}
	real, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", err
	}
	canonicalHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", err
	}
	if real != filepath.Join(canonicalHome, rel) {
		return "", errors.New("kubernetes: symlink mount source is forbidden")
	}
	return filepath.ToSlash(rel), nil
}

func (f *factory) CreateSession(ctx context.Context, p sandbox.Policy) (sandbox.Session, error) {
	if err := f.Supported(p); err != nil {
		return nil, err
	}
	if (f.generation == 0) != (f.executorBootID == "") || f.generation < 0 {
		return nil, errors.New("kubernetes: generation and executor boot must be supplied together")
	}
	c := f.client
	// Creation is serialized per server; parallelize only with per-owner fencing.
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.creationErr != nil {
		return nil, c.creationErr
	}
	if c.pending != nil {
		if err := c.pending.Close(); err != nil {
			return nil, fmt.Errorf("kubernetes: prior startup cleanup remains unfenced: %w", err)
		}
		c.pending = nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.StartupTimeout)
	defer cancel()
	id := sandbox.NewSessionID()
	tempBase := filepath.Join(c.cfg.StellaHome, "tmp", "kubernetes", string(c.pvc.UID))
	if err := os.MkdirAll(tempBase, 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(tempBase, id+"-")
	if err != nil {
		return nil, err
	}
	var mounts []sessionfs.Mount
	var volumes []core.VolumeMount
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(tmp)
		}
	}()
	for _, m := range p.Filesystem.Mounts {
		if !path.IsAbs(m.SandboxPath) || path.Clean(m.SandboxPath) != m.SandboxPath || m.SandboxPath == "/" || m.SandboxPath == "/opt/stella" || m.SandboxPath == "/tmp" {
			return nil, errors.New("kubernetes: invalid or reserved mount target")
		}
		if m.Access != sandbox.MountReadOnly && m.Access != sandbox.MountReadWrite {
			return nil, errors.New("kubernetes: invalid mount access")
		}

		// The image owns the shared mise tree, but the core selection at
		// /opt/stella/bin is a verified Linux projection prepared on the same
		// Kubernetes node and must be mounted for EnvCoreRuntimeDir to resolve.
		if m.SandboxPath == "/opt/stella/.mise-tools" {
			if m.Access != sandbox.MountReadOnly {
				return nil, errors.New("kubernetes: image runtime mounts must be read-only")
			}
			continue
		}
		source := f.sources[m.SandboxPath]
		if m.SandboxPath == sandbox.MountBuiltinSkills && (m.Access != sandbox.MountReadOnly || filepath.Base(source) != c.cfg.BundleRevision) {
			return nil, errors.New("kubernetes: host bundle projection revision mismatch")
		}
		rel, err := subPath(c.cfg.StellaHome, source)
		if err != nil {
			return nil, fmt.Errorf("kubernetes: mount %s: %w", m.SandboxPath, err)
		}
		ro := m.Access == sandbox.MountReadOnly
		mounts = append(mounts, sessionfs.Mount{HostPath: source, SandboxPath: m.SandboxPath, ReadOnly: ro})
		if m.SandboxPath != sandbox.MountBuiltinSkills {
			volumes = append(volumes, core.VolumeMount{Name: "home", MountPath: m.SandboxPath, SubPath: path.Join(c.volumePrefix, rel), ReadOnly: ro})
		}
	}
	rel, err := subPath(c.cfg.StellaHome, tmp)
	if err != nil {
		return nil, err
	}
	mounts = append(mounts, sessionfs.Mount{HostPath: tmp, SandboxPath: "/tmp"})
	volumes = append(volumes, core.VolumeMount{Name: "home", MountPath: "/tmp", SubPath: path.Join(c.volumePrefix, rel)})
	resolver, err := sessionfs.NewResolver(p.Filesystem.WorkingDir, mounts)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !keep {
			_ = resolver.Close()
		}
	}()
	p.Env = maps.Clone(p.Env)
	if p.Env == nil {
		p.Env = map[string]string{}
	}
	p.InheritEnv = false
	sharedData := ""
	for _, m := range p.Filesystem.Mounts {
		if m.SandboxPath == sandbox.MountUserData {
			sharedData = sandbox.MountUserData
		}
	}
	if err := sandbox.ApplyFilesystemEnv(p.Env, sandbox.FilesystemView{Home: sandbox.MountWorkspace, SharedDataDir: sharedData, TempDir: "/tmp"}); err != nil {
		return nil, err
	}
	p.Env["STELLA_HOME"] = "/opt/stella"
	if p.NetworkModeOrDefault() == sandbox.NetworkDisabled {
		delete(p.Env, "STELLA_SERVER_URL")
	} else if c.cfg.ServerURL != "" {
		p.Env["STELLA_SERVER_URL"] = c.cfg.ServerURL
	}
	p.Filesystem.Mounts = sessionfs.PolicyMounts(mounts)
	bootLabel, generationLabel := c.boot, id
	managedGeneration := f.generation > 0
	if managedGeneration {
		bootLabel = f.executorBootID
		generationLabel = strconv.FormatInt(f.generation, 10)
	}
	labels := map[string]string{labelStorage: string(c.pvc.UID), labelBoot: bootLabel, labelGeneration: generationLabel, "stella.cherryhq.io/network": string(p.NetworkModeOrDefault())}
	if managedGeneration {
		labels[labelGenerationManaged] = generationManagedValue
	}
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "stella-sandbox-" + id, Namespace: c.owner.Namespace, Labels: labels, Finalizers: []string{finalizer}, OwnerReferences: []meta.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: c.owner.Name, UID: c.owner.UID}}}, Spec: core.PodSpec{
		ImagePullSecrets: c.owner.Spec.ImagePullSecrets, RestartPolicy: core.RestartPolicyNever, AutomountServiceAccountToken: ptr.To(false), EnableServiceLinks: ptr.To(false), ServiceAccountName: "stella-sandbox", TerminationGracePeriodSeconds: ptr.To(int64(1)),
		SecurityContext: &core.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(1000)), RunAsGroup: ptr.To(int64(1000)), SeccompProfile: &core.SeccompProfile{Type: core.SeccompProfileTypeRuntimeDefault}},
		Affinity:        &core.Affinity{NodeAffinity: &core.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &core.NodeSelector{NodeSelectorTerms: []core.NodeSelectorTerm{{MatchFields: []core.NodeSelectorRequirement{{Key: "metadata.name", Operator: core.NodeSelectorOpIn, Values: []string{c.owner.Spec.NodeName}}}}}}}},
		Volumes:         []core.Volume{{Name: "home", VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: c.pvc.Name}}}},
		Containers:      []core.Container{{Name: "sandbox", Image: c.cfg.Image, Command: []string{"/usr/bin/sleep", "infinity"}, VolumeMounts: volumes, SecurityContext: &core.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &core.Capabilities{Drop: []core.Capability{"ALL"}}}, Resources: core.ResourceRequirements{Requests: core.ResourceList{core.ResourceCPU: resource.MustParse("100m"), core.ResourceMemory: resource.MustParse("128Mi")}, Limits: core.ResourceList{core.ResourceCPU: resource.MustParse("2"), core.ResourceMemory: resource.MustParse("2Gi"), core.ResourceEphemeralStorage: resource.MustParse("1Gi")}}}},
	}}
	if err = resolver.ValidateBackingPaths(); err != nil {
		return nil, err
	}
	created, err := c.api.CoreV1().Pods(c.owner.Namespace).Create(ctx, pod, meta.CreateOptions{})
	if err != nil {
		if !apierrors.IsInvalid(err) && !apierrors.IsForbidden(err) && !apierrors.IsUnauthorized(err) && !apierrors.IsAlreadyExists(err) {
			keep = true
			_ = resolver.Close()
			c.creationErr = errors.New("kubernetes: Pod creation outcome unknown; restart server to reconcile before creating more sessions")
		}
		return nil, err
	}
	s := &session{client: c, pod: created, policy: p, sources: maps.Clone(f.sources), resolver: resolver, files: sessionfs.NewAccessWithTempDir(resolver, "/tmp"), tmp: tmp, done: make(chan struct{})}
	s.policy.Env = s.environment(nil, sandbox.EnvOverlay)
	keep = true // Never remove backing storage before execution has been fenced.
	err = wait.PollUntilContextCancel(ctx, 200*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		got, err := c.api.CoreV1().Pods(c.owner.Namespace).Get(ctx, pod.Name, meta.GetOptions{})
		if err != nil {
			return false, err
		}
		if got.UID != created.UID {
			return false, errors.New("kubernetes: startup UID changed")
		}
		if got.Status.Phase == core.PodFailed || got.Status.Phase == core.PodSucceeded {
			return false, errors.New("kubernetes: sandbox exited during startup")
		}
		for _, status := range got.Status.ContainerStatuses {
			if status.Name == "sandbox" && status.Ready {
				return true, nil
			}
		}
		return false, nil
	})
	if err == nil {
		out := sandbox.NewExecOutputBuffer()
		err = c.stream(ctx, created, []string{"/usr/bin/readlink", "/opt/stella/skills/builtin"}, nil, out, nil)
		if err == nil && strings.TrimSpace(out.String()) != "../bundles/"+c.cfg.BundleRevision {
			err = errors.New("kubernetes: image bundle revision mismatch")
		}
	}
	if err != nil {
		closeErr := s.Close()
		if closeErr != nil {
			c.pending = s
		}
		return nil, errors.Join(err, closeErr)
	}
	go s.watch()
	return s, nil
}

type session struct {
	client   *Client
	pod      *core.Pod
	policy   sandbox.Policy
	sources  map[string]string
	resolver *sessionfs.Resolver
	files    sandbox.FileAccess
	tmp      string
	mu       sync.Mutex
	closed   bool
	invalid  bool
	done     chan struct{}
}

func (s *session) Policy() sandbox.Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.policy
	p.Env = maps.Clone(p.Env)
	return p
}
func (s *session) WorkingDir() string        { return s.policy.Filesystem.WorkingDir }
func (s *session) Files() sandbox.FileAccess { return guardedFiles{s} }

// Alive stays true while termination is uncertain, preventing resilient recreation.
func (s *session) Alive() bool           { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed }
func (s *session) Done() <-chan struct{} { return s.done }
func (s *session) RefreshEnv(env map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policy.Env = s.environment(env, sandbox.EnvOverlay)
}

func (s *session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.invalid = true
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.client.deletePod(ctx, s.pod.Name, s.pod.UID); err != nil {
		return err
	}
	err := errors.Join(s.resolver.Close(), os.RemoveAll(s.tmp))
	s.closed = true
	close(s.done)
	return err
}

func (s *session) watch() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.mu.Lock()
			invalid := s.invalid
			s.mu.Unlock()
			if invalid {
				_ = s.Close()
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			p, err := s.client.api.CoreV1().Pods(s.client.owner.Namespace).Get(ctx, s.pod.Name, meta.GetOptions{})
			cancel()
			if err != nil {
				continue
			}
			if p.UID != s.pod.UID || p.DeletionTimestamp != nil || p.Status.Phase == core.PodFailed || p.Status.Phase == core.PodSucceeded {
				_ = s.Close()
			}
		}
	}
}
