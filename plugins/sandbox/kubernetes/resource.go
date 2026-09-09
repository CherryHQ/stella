package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	sandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

const resourceBackend = "kubernetes"

var (
	// ErrResourceAuthorityMismatch means the identity belongs to another
	// validated deployment PVC or namespace. A missing Pod under that identity
	// cannot be treated as cleanup proof.
	ErrResourceAuthorityMismatch = errors.New("kubernetes resource authority mismatch")
	// ErrResourceIdentityMismatch means the API returned a different immutable
	// Pod UID than the one recorded for this generation.
	ErrResourceIdentityMismatch = errors.New("kubernetes resource identity mismatch")
)

// resourceAuthority binds a persisted resource identity to the deployment's
// validated home PVC and namespace. The PVC UID is the deployment control
// domain already authorized by this backend, and remains stable across server
// restarts without trusting a reusable Kubernetes service hostname.
type resourceAuthority struct {
	Namespace string `json:"namespace"`
	PVCUID    string `json:"pvc_uid"`
}

// resourceRef contains the mutable locator and immutable identity of a Pod.
// Names are only a lookup aid. Every operation must also match UID.
type resourceRef struct {
	Name string    `json:"name"`
	UID  types.UID `json:"uid"`
}

func encodeResourceAuthority(namespace, pvcUID string) (string, error) {
	if namespace == "" || pvcUID == "" {
		return "", errors.New("kubernetes: deployment API identity is incomplete")
	}
	authority, err := json.Marshal(resourceAuthority{Namespace: namespace, PVCUID: pvcUID})
	if err != nil {
		return "", fmt.Errorf("kubernetes: encode API identity: %w", err)
	}
	return string(authority), nil
}

func encodeResourceRef(pod *core.Pod) (string, error) {
	if pod == nil || pod.Name == "" || pod.UID == "" {
		return "", errors.New("kubernetes: Pod identity is incomplete")
	}
	ref, err := json.Marshal(resourceRef{Name: pod.Name, UID: pod.UID})
	if err != nil {
		return "", fmt.Errorf("kubernetes: encode Pod identity: %w", err)
	}
	return string(ref), nil
}

func decodeResourceAuthority(raw string) (resourceAuthority, error) {
	var authority resourceAuthority
	if err := json.Unmarshal([]byte(raw), &authority); err != nil {
		return resourceAuthority{}, errors.New("kubernetes: malformed API identity")
	}
	if authority.Namespace == "" || authority.PVCUID == "" {
		return resourceAuthority{}, errors.New("kubernetes: incomplete API identity")
	}
	return authority, nil
}

func decodeResourceRef(raw string) (resourceRef, error) {
	var ref resourceRef
	if err := json.Unmarshal([]byte(raw), &ref); err != nil {
		return resourceRef{}, errors.New("kubernetes: malformed Pod identity")
	}
	if ref.Name == "" || ref.UID == "" {
		return resourceRef{}, errors.New("kubernetes: incomplete Pod identity")
	}
	return ref, nil
}

// ResourceIdentity returns the immutable Pod identity for this raw session.
// It remains available after Close so a caller can retain the identity while
// a failed termination is reconciled.
func (s *session) ResourceIdentity(ctx context.Context) (sandbox.ResourceIdentity, error) {
	if err := ctx.Err(); err != nil {
		return sandbox.ResourceIdentity{}, err
	}
	s.mu.Lock()
	pod := s.pod.DeepCopy()
	client := s.client
	s.mu.Unlock()
	if pod == nil || client == nil {
		return sandbox.ResourceIdentity{}, errors.New("kubernetes: session identity is unavailable")
	}
	if client.authority == "" {
		return sandbox.ResourceIdentity{}, errors.New("kubernetes: deployment API identity is unavailable")
	}
	ref, err := encodeResourceRef(pod)
	if err != nil {
		return sandbox.ResourceIdentity{}, err
	}
	return sandbox.ResourceIdentity{Backend: resourceBackend, Authority: client.authority, Ref: ref}, nil
}

// ResourceController returns a reconstructible controller bound to this
// deployment's validated home PVC identity. It performs no resource operation
// until Probe or Terminate.
func (f *factory) ResourceController(ctx context.Context) (sandbox.ResourceController, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f == nil || f.client == nil || f.client.api == nil || f.client.authority == "" {
		return nil, errors.New("kubernetes: resource controller is unavailable")
	}
	authority, err := decodeResourceAuthority(f.client.authority)
	if err != nil {
		return nil, err
	}
	return &resourceController{client: f.client, namespace: authority.Namespace, authority: f.client.authority}, nil
}

type resourceController struct {
	client    *Client
	namespace string
	authority string
}

func (c *resourceController) resourceRef(identity sandbox.ResourceIdentity) (resourceRef, error) {
	if c == nil || c.client == nil || c.client.api == nil {
		return resourceRef{}, errors.New("kubernetes: resource controller is unavailable")
	}
	if identity.Backend != resourceBackend {
		return resourceRef{}, fmt.Errorf("kubernetes: resource backend %q is not %q", identity.Backend, resourceBackend)
	}
	authority, err := decodeResourceAuthority(identity.Authority)
	if err != nil {
		return resourceRef{}, err
	}
	expected, err := decodeResourceAuthority(c.authority)
	if err != nil {
		return resourceRef{}, err
	}
	if authority != expected {
		return resourceRef{}, fmt.Errorf("%w: deployment authority changed", ErrResourceAuthorityMismatch)
	}
	ref, err := decodeResourceRef(identity.Ref)
	if err != nil {
		return resourceRef{}, err
	}
	return ref, nil
}

func unknownObservation(err error) (sandbox.ResourceObservation, error) {
	if err == nil {
		err = errors.New("kubernetes: resource state is unknown")
	}
	return sandbox.ResourceObservation{State: sandbox.ResourceStateUnknown, Detail: err.Error()}, err
}

func (c *resourceController) Probe(ctx context.Context, identity sandbox.ResourceIdentity) (sandbox.ResourceObservation, error) {
	ref, err := c.resourceRef(identity)
	if err != nil {
		return unknownObservation(err)
	}
	pod, err := c.client.api.CoreV1().Pods(c.namespace).Get(ctx, ref.Name, meta.GetOptions{})
	if apierrors.IsNotFound(err) {
		return unknownObservation(errors.New("kubernetes: Pod absence is not termination proof"))
	}
	if err != nil {
		return unknownObservation(fmt.Errorf("kubernetes: probe Pod: %w", err))
	}
	if pod.UID != ref.UID {
		return unknownObservation(fmt.Errorf("%w: probe Pod UID changed", ErrResourceIdentityMismatch))
	}
	return sandbox.ResourceObservation{State: sandbox.ResourceStatePresent, Detail: "Pod is present"}, nil
}

func (c *resourceController) Terminate(ctx context.Context, identity sandbox.ResourceIdentity) (sandbox.ResourceObservation, error) {
	ref, err := c.resourceRef(identity)
	if err != nil {
		return unknownObservation(err)
	}
	return c.client.deletePodObservation(ctx, c.namespace, ref.Name, ref.UID)
}

var (
	_ sandbox.ResourceIdentityProvider = (*session)(nil)
	_ interface {
		ResourceController(context.Context) (sandbox.ResourceController, error)
	} = (*factory)(nil)
)
