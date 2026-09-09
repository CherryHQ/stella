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

	sandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

const (
	labelStorage    = "stella.cherryhq.io/storage"
	labelBoot       = "stella.cherryhq.io/boot"
	labelGeneration = "stella.cherryhq.io/generation"
	// labelGenerationManaged marks Pods owned by the durable generation store.
	// Startup cleanup must leave these resources for exact-generation fencing.
	labelGenerationManaged = "stella.cherryhq.io/generation-managed"
	generationManagedValue = "true"
)

// deletePod keeps a finalizer until kubelet reports terminal execution. Object
// disappearance alone cannot fence a partitioned node or an external force delete.
func (c *Client) deletePod(ctx context.Context, name string, uid types.UID) error {
	observation, err := c.deletePodObservation(ctx, c.owner.Namespace, name, uid)
	if err != nil {
		return err
	}
	if observation.State != sandbox.ResourceStateAbsent {
		return errors.New("kubernetes: execution termination remains unconfirmed")
	}
	return nil
}

// deletePodObservation is the one Kubernetes termination proof used by both
// raw sessions and the durable controller. A terminal Pod (or a deletion of an
// unscheduled Pod) is the proof; a missing object is not.
func (c *Client) deletePodObservation(ctx context.Context, namespace, name string, uid types.UID) (sandbox.ResourceObservation, error) {
	pods := c.api.CoreV1().Pods(namespace)
	p, err := pods.Get(ctx, name, meta.GetOptions{})
	if err != nil {
		return unknownObservation(fmt.Errorf("kubernetes: cannot establish execution termination: %w", err))
	}
	if p.UID != uid {
		return unknownObservation(fmt.Errorf("%w: Pod UID changed before termination", ErrResourceIdentityMismatch))
	}
	grace := int64(1)
	if err = pods.Delete(ctx, name, meta.DeleteOptions{GracePeriodSeconds: &grace, Preconditions: &meta.Preconditions{UID: &uid}}); err != nil {
		return unknownObservation(fmt.Errorf("kubernetes: request Pod termination: %w", err))
	}
	observation := sandbox.ResourceObservation{State: sandbox.ResourceStateUnknown, Detail: "termination pending"}
	err = wait.PollUntilContextCancel(ctx, 200*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		p, err := pods.Get(ctx, name, meta.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, errors.New("kubernetes: termination observation disappeared before terminal proof")
		}
		if err != nil {
			return false, fmt.Errorf("kubernetes: termination unconfirmed: %w", err)
		}
		if p.UID != uid {
			return false, fmt.Errorf("%w: termination Pod UID changed", ErrResourceIdentityMismatch)
		}
		if p.Status.Phase != core.PodSucceeded && p.Status.Phase != core.PodFailed && (p.Spec.NodeName != "" || p.DeletionTimestamp == nil) {
			return false, nil
		}
		// The backend's own finalizer is the only one it may release.
		remaining := slices.DeleteFunc(slices.Clone(p.Finalizers), func(value string) bool { return value == finalizer })
		patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"uid": uid, "resourceVersion": p.ResourceVersion, "finalizers": remaining}})
		if err != nil {
			return false, fmt.Errorf("kubernetes: encode termination fence: %w", err)
		}
		_, err = pods.Patch(ctx, name, types.MergePatchType, patch, meta.PatchOptions{})
		if apierrors.IsConflict(err) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("kubernetes: release termination finalizer: %w", err)
		}
		observation = sandbox.ResourceObservation{State: sandbox.ResourceStateAbsent, Detail: "Pod reached terminal state under exact UID"}
		return true, nil
	})
	if err != nil {
		return unknownObservation(err)
	}
	return observation, nil
}

func (c *Client) cleanupPreviousBoot(ctx context.Context) error {
	pods, err := c.api.CoreV1().Pods(c.owner.Namespace).List(ctx, meta.ListOptions{LabelSelector: labelStorage + "=" + string(c.pvc.UID)})
	if err != nil {
		return err
	}
	for _, p := range pods.Items {
		// Durable generations are reconciled from PostgreSQL by their exact
		// identity. This legacy pass must never delete one based on boot age.
		if p.Labels[labelGenerationManaged] != "" {
			continue
		}
		if p.Labels[labelBoot] == c.boot {
			continue
		}
		if len(p.OwnerReferences) != 1 || p.OwnerReferences[0].Kind != "Pod" {
			return errors.New("kubernetes: ambiguous sandbox ownership")
		}
		owner := p.OwnerReferences[0]
		if owner.UID != c.owner.UID {
			old, err := c.api.CoreV1().Pods(c.owner.Namespace).Get(ctx, owner.Name, meta.GetOptions{})
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
