// Package kubernetes runs each sandbox generation in a same-node Pod backed by
// authorized subdirectories of the server's shared PVC.
package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
 sandbox "github.com/CherryHQ/stella/pkg/sandbox"

	"k8s.io/apimachinery/pkg/util/validation"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Config is deployment-owned; agents cannot supply Kubernetes object fields.
type Config struct {
	Namespace, OwnerName, NodeName, Deployment, PVC, Image, StellaHome, BundleRevision, ServerURL string
	OwnerUID                                                                                      types.UID
	StartupTimeout                                                                                time.Duration
}

// Client owns one deployment connection and boot identity. Reuse it across factories.
type Client struct {
	api          kubernetes.Interface
	rest         *rest.Config
	cfg          Config
	boot         string
	volumePrefix string
	mu           sync.Mutex
	pending      map[string]*session
	creationErr  error
	pullSecrets  []core.LocalObjectReference
}

func NewInCluster(ctx context.Context, cfg Config) (*Client, error) {
	rc, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("kubernetes: in-cluster identity: %w", err)
	}
	return NewClient(ctx, cfg, rc)
}

func NewClient(ctx context.Context, cfg Config, rc *rest.Config) (*Client, error) {
	for name, value := range map[string]string{"namespace": cfg.Namespace, "owner name": cfg.OwnerName, "owner UID": string(cfg.OwnerUID), "node": cfg.NodeName, "deployment": cfg.Deployment, "PVC": cfg.PVC, "image": cfg.Image, "home": cfg.StellaHome, "bundle revision": cfg.BundleRevision} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("kubernetes: %s is required", name)
		}
	}
	u, parseErr := url.Parse(cfg.ServerURL)
	if parseErr != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback() {
		return nil, errors.New("kubernetes: server URL must be a non-loopback http(s) service URL")
	}
	if cfg.StartupTimeout <= 0 {
		cfg.StartupTimeout = 120 * time.Second
	}
	if len(validation.IsDNS1123Label(cfg.Deployment)) != 0 {
		return nil, errors.New("kubernetes: deployment must be a DNS label")
	}
	api, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	c := &Client{api: api, rest: rest.CopyConfig(rc), cfg: cfg, boot: sandbox.NewSessionID(), pending: map[string]*session{}}
	owner, err := api.CoreV1().Pods(cfg.Namespace).Get(ctx, cfg.OwnerName, meta.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("kubernetes: owner: %w", err)
	}
	if owner.UID != cfg.OwnerUID || owner.Spec.NodeName != cfg.NodeName || owner.DeletionTimestamp != nil {
		return nil, errors.New("kubernetes: owner identity or node mismatch")
	}
	c.pullSecrets = append([]core.LocalObjectReference(nil), owner.Spec.ImagePullSecrets...)
	found := false
	for _, container := range owner.Spec.Containers {
		for _, mount := range container.VolumeMounts {
			rel, relErr := filepath.Rel(mount.MountPath, cfg.StellaHome)
			if relErr != nil || !filepath.IsLocal(rel) || mount.ReadOnly || mount.SubPathExpr != "" {
				continue
			}
			for _, volume := range owner.Spec.Volumes {
				if volume.Name == mount.Name && volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == cfg.PVC {
					c.volumePrefix = path.Join(mount.SubPath, filepath.ToSlash(rel))
					found = true
				}
			}
		}
	}
	if !found {
		return nil, errors.New("kubernetes: server home is not backed by configured PVC")
	}
	pvc, err := api.CoreV1().PersistentVolumeClaims(cfg.Namespace).Get(ctx, cfg.PVC, meta.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("kubernetes: PVC: %w", err)
	}
	if slices.Contains(pvc.Spec.AccessModes, core.ReadWriteOncePod) {
		return nil, errors.New("kubernetes: shared PVC cannot use ReadWriteOncePod")
	}
	if err = c.cleanupPreviousBoot(ctx); err != nil {
		return nil, err
	}
	if err = os.RemoveAll(filepath.Join(cfg.StellaHome, "tmp", "kubernetes", cfg.Deployment)); err != nil {
		return nil, err
	}
	return c, nil
}
