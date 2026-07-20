package dockerclient

const (
	LabelSessionID = "stella.sandbox.session_id"
	// LabelStellaHome scopes orphan cleanup. It stores a daemon-visible STELLA_HOME
	// path for bind mounts, or "volume:<name>" for named-volume mode.
	LabelStellaHome = "stella.sandbox.stella_home"
	LabelCreatedAt  = "stella.sandbox.created_at" // RFC3339

	// LabelOwnerKind and LabelOwnerID identify the stellad instance that created
	// a sandbox. New containers always write these as a pair.
	LabelOwnerKind = "stella.sandbox.owner_kind"
	LabelOwnerID   = "stella.sandbox.owner_id"

	// LabelOwnerPID is retained only to read containers created before owner
	// identities were introduced. Never write this label on new containers.
	LabelOwnerPID = "stella.sandbox.owner_pid"
)

const (
	OwnerKindProcess   = "process"
	OwnerKindContainer = "container"
)

// Owner identifies the current stellad instance for orphan cleanup. It is
// constructed by the docker backend once at factory construction.
type Owner struct {
	Kind string
	ID   string
}
