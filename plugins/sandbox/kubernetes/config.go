// Package kubernetes runs each sandbox generation in a same-node Pod backed by
// authorized subdirectories of the server's shared PVC.
package kubernetes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	sandbox "github.com/CherryHQ/stella/pkg/sandbox"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Config is deployment-owned; agents cannot supply Kubernetes object fields.
type Config struct {
	Image, StellaHome, BundleRevision, ServerURL string
	ServerPort                                   int
	StartupTimeout                               time.Duration
}

// Client owns one deployment connection and boot identity. Reuse it across factories.
type Client struct {
	api          kubernetes.Interface
	rest         *rest.Config
	cfg          Config
	boot         string
	owner        *core.Pod
	pvc          *core.PersistentVolumeClaim
	volumePrefix string
	mu           sync.Mutex
	pending      *session // Only a failed startup can remain unowned by a caller.
	creationErr  error
}

func NewInCluster(ctx context.Context, cfg Config) (*Client, error) {
	rc, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("kubernetes: in-cluster identity: %w", err)
	}
	return NewClient(ctx, cfg, rc)
}

func NewClient(ctx context.Context, cfg Config, rc *rest.Config) (*Client, error) {
	for name, value := range map[string]string{"image": cfg.Image, "home": cfg.StellaHome, "bundle revision": cfg.BundleRevision} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("kubernetes: %s is required", name)
		}
	}
	if cfg.StartupTimeout <= 0 {
		cfg.StartupTimeout = 120 * time.Second
	}
	identity, err := podIdentity(rc.BearerToken)
	if err != nil {
		return nil, err
	}
	api, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	c := &Client{api: api, rest: rest.CopyConfig(rc), boot: sandbox.NewSessionID()}
	owner, err := api.CoreV1().Pods(identity.Namespace).Get(ctx, identity.Name, meta.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("kubernetes: owner: %w", err)
	}
	if owner.UID != identity.UID || owner.DeletionTimestamp != nil {
		return nil, errors.New("kubernetes: owner Pod identity changed or is terminating")
	}
	c.owner = owner
	if cfg.ServerURL == "" {
		cfg.ServerURL = "http://" + net.JoinHostPort(owner.Status.PodIP, strconv.Itoa(cfg.ServerPort))
	}
	u, parseErr := url.Parse(cfg.ServerURL)
	if parseErr != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback() {
		return nil, errors.New("kubernetes: server URL must be a non-loopback http(s) URL")
	}
	c.cfg = cfg
	claim, prefix, err := ownerHomeVolume(owner, cfg.StellaHome)
	if err != nil {
		return nil, err
	}
	pvc, err := api.CoreV1().PersistentVolumeClaims(owner.Namespace).Get(ctx, claim, meta.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("kubernetes: PVC: %w", err)
	}
	if slices.Contains(pvc.Spec.AccessModes, core.ReadWriteOncePod) {
		return nil, errors.New("kubernetes: shared PVC cannot use ReadWriteOncePod")
	}
	c.pvc = pvc
	c.volumePrefix = prefix
	if err = c.cleanupPreviousBoot(ctx); err != nil {
		return nil, err
	}
	if err = os.RemoveAll(filepath.Join(cfg.StellaHome, "tmp", "kubernetes", string(c.pvc.UID))); err != nil {
		return nil, err
	}
	return c, nil
}

// The projected token is read locally by InClusterConfig. These claims locate
// our Pod; the API server authenticates the token when we fetch it.
func podIdentity(token string) (core.ObjectReference, error) {
	invalid := errors.New("kubernetes: projected Pod-bound service account token required")
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return core.ObjectReference{}, invalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return core.ObjectReference{}, invalid
	}
	var claims struct {
		Kubernetes struct {
			Namespace string               `json:"namespace"`
			Pod       core.ObjectReference `json:"pod"`
		} `json:"kubernetes.io"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return core.ObjectReference{}, invalid
	}
	owner := claims.Kubernetes.Pod
	owner.Namespace = claims.Kubernetes.Namespace
	if owner.Namespace == "" || owner.Name == "" || owner.UID == "" {
		return core.ObjectReference{}, invalid
	}
	return owner, nil
}

// Discover the one PVC mounted directly at /data, including when sidecars share
// that mount. Different /data claims are ambiguous without a container identity.
func ownerHomeVolume(owner *core.Pod, home string) (string, string, error) {
	rel, err := filepath.Rel("/data", home)
	if err != nil || !filepath.IsLocal(rel) {
		return "", "", errors.New("kubernetes: home must be under /data")
	}
	invalid := errors.New("kubernetes: mount the home PVC directly at /data without subPath")
	claim := ""
	writable := false
	for _, container := range owner.Spec.Containers {
		for _, mount := range container.VolumeMounts {
			if mount.MountPath != "/data" {
				continue
			}
			if mount.SubPath != "" || mount.SubPathExpr != "" {
				return "", "", invalid
			}
			index := slices.IndexFunc(owner.Spec.Volumes, func(v core.Volume) bool { return v.Name == mount.Name })
			if index < 0 || owner.Spec.Volumes[index].PersistentVolumeClaim == nil {
				return "", "", invalid
			}
			volume := owner.Spec.Volumes[index].PersistentVolumeClaim
			if claim != "" && claim != volume.ClaimName {
				return "", "", errors.New("kubernetes: multiple PVCs mounted at /data")
			}
			claim = volume.ClaimName
			writable = writable || (!mount.ReadOnly && !volume.ReadOnly)
		}
	}
	if claim == "" || !writable {
		return "", "", invalid
	}
	return claim, filepath.ToSlash(rel), nil
}
