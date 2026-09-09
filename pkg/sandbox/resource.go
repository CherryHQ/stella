package sandbox

import "context"

// ResourceIdentity names one backend-owned compute resource independently of
// the raw Session that created it. A controller can therefore reconstruct its
// authority after the creating executor exits.
type ResourceIdentity struct {
	Backend   string `json:"backend"`
	Authority string `json:"authority"`
	Ref       string `json:"ref"`
}

// ResourceState is the result of an observation about a resource. Unknown is
// deliberately distinct from absent: a caller must keep an unproven resource
// fenced instead of treating an incomplete observation as cleanup proof.
type ResourceState string

const (
	ResourceStatePresent ResourceState = "present"
	ResourceStateAbsent  ResourceState = "absent"
	ResourceStateUnknown ResourceState = "unknown"
)

// ResourceObservation records what a backend can prove about a resource.
type ResourceObservation struct {
	State  ResourceState `json:"state"`
	Detail string        `json:"detail"`
}

// ResourceIdentityProvider exposes the durable identity of a backend-owned
// resource. The identity must be sufficient for a separately reconstructed
// controller to address the resource without the original Session object.
type ResourceIdentityProvider interface {
	ResourceIdentity(context.Context) (ResourceIdentity, error)
}

// ResourceObservationProvider reports what the original raw session can prove
// about its provider resource after local cleanup. It is intentionally an
// optional capability: a restarted process has no original session and must
// keep a controllerless generation unknown.
type ResourceObservationProvider interface {
	ObserveResource(context.Context) (ResourceObservation, error)
}

// ResourceController probes and terminates backend-owned resources by durable
// identity. Terminate reports the post-operation observation; it does not
// imply absence unless its State is ResourceStateAbsent.
type ResourceController interface {
	Probe(context.Context, ResourceIdentity) (ResourceObservation, error)
	Terminate(context.Context, ResourceIdentity) (ResourceObservation, error)
}
