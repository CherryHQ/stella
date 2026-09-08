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
	Namespace, OwnerName, PVC, Image, StellaHome, BundleRevision, ServerURL string
	ServerPort                                                              int
	StartupTimeout                                                          time.Duration
}

// Client owns one deployment connection and boot identity. Reuse it across factories.
type Client struct {
	api          kubernetes.Interface
	rest         *rest.Config
	cfg          Config
	boot         string
	owner        *core.Pod
	storageID    string
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
	for name, value := range map[string]string{"namespace": cfg.Namespace, "owner name": cfg.OwnerName, "PVC": cfg.PVC, "image": cfg.Image, "home": cfg.StellaHome, "bundle revision": cfg.BundleRevision} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("kubernetes: %s is required", name)
		}
	}
	if cfg.StartupTimeout <= 0 {
		cfg.StartupTimeout = 120 * time.Second
	}
	api, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	c := &Client{api: api, rest: rest.CopyConfig(rc), boot: sandbox.NewSessionID()}
	owner, err := api.CoreV1().Pods(cfg.Namespace).Get(ctx, cfg.OwnerName, meta.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("kubernetes: owner: %w", err)
	}
	if owner.DeletionTimestamp != nil {
		return nil, errors.New("kubernetes: owner Pod is terminating")
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
	c.volumePrefix, err = ownerHomePrefix(owner, cfg)
	if err != nil {
		return nil, err
	}
	pvc, err := api.CoreV1().PersistentVolumeClaims(cfg.Namespace).Get(ctx, cfg.PVC, meta.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("kubernetes: PVC: %w", err)
	}
	if slices.Contains(pvc.Spec.AccessModes, core.ReadWriteOncePod) {
		return nil, errors.New("kubernetes: shared PVC cannot use ReadWriteOncePod")
	}
	c.storageID = string(pvc.UID)
	if err = c.cleanupPreviousBoot(ctx); err != nil {
		return nil, err
	}
	if err = os.RemoveAll(filepath.Join(cfg.StellaHome, "tmp", "kubernetes", c.storageID)); err != nil {
		return nil, err
	}
	return c, nil
}

// The first version supports one deployment layout: the home PVC is mounted
// directly at /data. Runtime/testbed homes may occupy subdirectories beneath it.
func ownerHomePrefix(owner *core.Pod, cfg Config) (string, error) {
	rel, err := filepath.Rel("/data", cfg.StellaHome)
	if err != nil || !filepath.IsLocal(rel) {
		return "", errors.New("kubernetes: home must be under /data")
	}
	for _, volume := range owner.Spec.Volumes {
		if volume.PersistentVolumeClaim == nil || volume.PersistentVolumeClaim.ClaimName != cfg.PVC {
			continue
		}
		for _, container := range owner.Spec.Containers {
			for _, mount := range container.VolumeMounts {
				if mount.Name == volume.Name && mount.MountPath == "/data" && !mount.ReadOnly && mount.SubPath == "" && mount.SubPathExpr == "" {
					return filepath.ToSlash(rel), nil
				}
			}
		}
	}
	return "", errors.New("kubernetes: mount the home PVC directly at /data without subPath")
}
