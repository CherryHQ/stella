package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	labelStorage = "stella.cherryhq.io/storage"
	labelBoot    = "stella.cherryhq.io/boot"
)

// deletePod keeps a finalizer until kubelet reports terminal execution. Object
// disappearance alone cannot fence a partitioned node or an external force delete.
func (c *Client) deletePod(ctx context.Context, name string, uid types.UID) error {
	p, err := c.api.CoreV1().Pods(c.cfg.Namespace).Get(ctx, name, meta.GetOptions{})
	if err != nil {
		return fmt.Errorf("kubernetes: cannot establish execution termination: %w", err)
	}
	if p.UID != uid {
		return errors.New("kubernetes: pod UID changed before termination")
	}
	grace := int64(1)
	if err = c.api.CoreV1().Pods(c.cfg.Namespace).Delete(ctx, name, meta.DeleteOptions{GracePeriodSeconds: &grace, Preconditions: &meta.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return wait.PollUntilContextCancel(ctx, 200*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		p, err := c.api.CoreV1().Pods(c.cfg.Namespace).Get(ctx, name, meta.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("kubernetes: termination unconfirmed: %w", err)
		}
		if p.UID != uid {
			return false, errors.New("kubernetes: termination UID mismatch")
		}
		if p.Status.Phase != core.PodSucceeded && p.Status.Phase != core.PodFailed && (p.Spec.NodeName != "" || p.DeletionTimestamp == nil) {
			return false, nil
		}
		// The backend's own finalizer is the only one it may release.
		remaining := slices.DeleteFunc(slices.Clone(p.Finalizers), func(value string) bool { return value == finalizer })
		patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"uid": uid, "resourceVersion": p.ResourceVersion, "finalizers": remaining}})
		_, err = c.api.CoreV1().Pods(c.cfg.Namespace).Patch(ctx, name, types.MergePatchType, patch, meta.PatchOptions{})
		if apierrors.IsConflict(err) {
			return false, nil
		}
		return err == nil, err
	})
}

func (c *Client) cleanupPreviousBoot(ctx context.Context) error {
	pods, err := c.api.CoreV1().Pods(c.cfg.Namespace).List(ctx, meta.ListOptions{LabelSelector: labelStorage + "=" + c.storageID})
	if err != nil {
		return err
	}
	for _, p := range pods.Items {
		if p.Labels[labelBoot] == c.boot {
			continue
		}
		if len(p.OwnerReferences) != 1 || p.OwnerReferences[0].Kind != "Pod" {
			return errors.New("kubernetes: ambiguous sandbox ownership")
		}
		owner := p.OwnerReferences[0]
		if owner.UID != c.owner.UID {
			old, err := c.api.CoreV1().Pods(c.cfg.Namespace).Get(ctx, owner.Name, meta.GetOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			if err == nil && old.UID == owner.UID {
				return errors.New("kubernetes: previous server Pod still exists; refusing concurrent generations")
			}
		}
		if err = c.deletePod(ctx, p.Name, p.UID); err != nil {
			return err
		}
	}
	return nil
}
