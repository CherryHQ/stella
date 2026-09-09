package dockerclient

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/containerd/errdefs"
	mobyclient "github.com/moby/moby/client"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

const resourceBackend = "docker"

var (
	// ErrResourceAuthorityMismatch means the requested resource belongs to a
	// different Docker daemon. Its absence on this daemon is not cleanup proof.
	ErrResourceAuthorityMismatch = errors.New("docker resource authority mismatch")
	// ErrResourceIdentityMismatch means Docker returned a different container
	// ID than the exact immutable reference that was requested.
	ErrResourceIdentityMismatch = errors.New("docker resource identity mismatch")
)

// ResourceController implements durable Docker resource fencing. It performs
// all observations against the daemon authority recorded in ResourceIdentity.
// A caller must treat ResourceStateUnknown as fenced, even when the returned
// error is nil.
type ResourceController struct {
	client *Client
}

// NewResourceController opens the configured Docker daemon and verifies its
// identity before returning a controller. The controller retains the client
// for the process lifetime because reconciliation may run after startup.
func NewResourceController(ctx context.Context) (sandboxpkg.ResourceController, error) {
	client, err := New()
	if err != nil {
		return nil, fmt.Errorf("docker resource controller: new client: %w", err)
	}
	if _, err := client.DaemonID(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client.Controller(), nil
}

// Controller returns a controller backed by this Docker client.
func (c *Client) Controller() *ResourceController {
	return &ResourceController{client: c}
}

var _ sandboxpkg.ResourceController = (*ResourceController)(nil)

func (r *ResourceController) Probe(ctx context.Context, identity sandboxpkg.ResourceIdentity) (sandboxpkg.ResourceObservation, error) {
	if err := validateResourceIdentity(identity); err != nil {
		return unknownObservation(err), err
	}
	if r == nil || r.client == nil {
		err := errors.New("docker resource controller is nil")
		return unknownObservation(err), err
	}
	if err := r.checkAuthority(ctx, identity.Authority); err != nil {
		return unknownObservation(err), err
	}
	inspected, err := r.client.api.ContainerInspect(ctx, identity.Ref, mobyclient.ContainerInspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			if err := r.checkAuthority(ctx, identity.Authority); err != nil {
				return unknownObservation(err), err
			}
			return sandboxpkg.ResourceObservation{State: sandboxpkg.ResourceStateAbsent, Detail: "container not found on the recorded daemon"}, nil
		}
		err = fmt.Errorf("docker resource inspect %s: %w", identity.Ref, err)
		return unknownObservation(err), err
	}
	if inspected.Container.ID != identity.Ref {
		err := fmt.Errorf("%w: requested %q, daemon returned %q", ErrResourceIdentityMismatch, identity.Ref, inspected.Container.ID)
		return unknownObservation(err), err
	}
	if err := r.checkAuthority(ctx, identity.Authority); err != nil {
		return unknownObservation(err), err
	}
	return sandboxpkg.ResourceObservation{State: sandboxpkg.ResourceStatePresent, Detail: "container exists on the recorded daemon"}, nil
}

func (r *ResourceController) Terminate(ctx context.Context, identity sandboxpkg.ResourceIdentity) (sandboxpkg.ResourceObservation, error) {
	if err := validateResourceIdentity(identity); err != nil {
		return unknownObservation(err), err
	}
	if r == nil || r.client == nil {
		err := errors.New("docker resource controller is nil")
		return unknownObservation(err), err
	}
	if err := r.checkAuthority(ctx, identity.Authority); err != nil {
		return unknownObservation(err), err
	}
	// Inspect first. Docker accepts abbreviated IDs and names, so this exact
	// identity check is the guard against terminating a different resource.
	inspected, err := r.client.api.ContainerInspect(ctx, identity.Ref, mobyclient.ContainerInspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			if err := r.checkAuthority(ctx, identity.Authority); err != nil {
				return unknownObservation(err), err
			}
			return sandboxpkg.ResourceObservation{State: sandboxpkg.ResourceStateAbsent, Detail: "container already absent on the recorded daemon"}, nil
		}
		err = fmt.Errorf("docker resource inspect %s before terminate: %w", identity.Ref, err)
		return unknownObservation(err), err
	}
	if inspected.Container.ID != identity.Ref {
		err := fmt.Errorf("%w: requested %q, daemon returned %q", ErrResourceIdentityMismatch, identity.Ref, inspected.Container.ID)
		return unknownObservation(err), err
	}

	timeout := 2
	if _, err := r.client.api.ContainerStop(ctx, identity.Ref, mobyclient.ContainerStopOptions{Timeout: &timeout}); err != nil && !errdefs.IsNotFound(err) {
		err = fmt.Errorf("docker resource stop %s: %w", identity.Ref, err)
		return unknownObservation(err), err
	}
	if _, err := r.client.api.ContainerRemove(ctx, identity.Ref, mobyclient.ContainerRemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
		err = fmt.Errorf("docker resource remove %s: %w", identity.Ref, err)
		return unknownObservation(err), err
	}

	// A successful API call is not itself the proof. Re-observe the exact ID so
	// a daemon that accepted but failed to complete removal remains fenced.
	return r.Probe(ctx, identity)
}

func (r *ResourceController) checkAuthority(ctx context.Context, want string) error {
	got, err := r.client.DaemonID(ctx)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%w: want %q, got %q", ErrResourceAuthorityMismatch, want, got)
	}
	return nil
}

func validateResourceIdentity(identity sandboxpkg.ResourceIdentity) error {
	if identity.Backend != resourceBackend {
		return fmt.Errorf("unsupported resource backend %q", identity.Backend)
	}
	if identity.Authority == "" {
		return errors.New("docker resource identity has empty daemon authority")
	}
	if identity.Ref == "" {
		return errors.New("docker resource identity has empty container reference")
	}
	if len(identity.Ref) != 64 {
		return errors.New("docker resource identity container reference must be a full 64-character ID")
	}
	if _, err := hex.DecodeString(identity.Ref); err != nil {
		return fmt.Errorf("docker resource identity container reference is not a full hexadecimal ID: %w", err)
	}
	return nil
}

func unknownObservation(err error) sandboxpkg.ResourceObservation {
	return sandboxpkg.ResourceObservation{State: sandboxpkg.ResourceStateUnknown, Detail: err.Error()}
}
