package dockerclient

const (
	LabelSessionID = "stella.sandbox.session_id"
	// LabelStellaHome scopes orphan cleanup. It stores a daemon-visible STELLA_HOME
	// path for bind mounts, or "volume:<name>" for named-volume mode.
	LabelStellaHome = "stella.sandbox.stella_home"
	LabelCreatedAt  = "stella.sandbox.created_at" // RFC3339
	LabelOwnerPID   = "stella.sandbox.owner_pid"  // PID of the creating stella process
	// LabelGeneration and LabelOwnerBootID bind a provider resource to the
	// durable SessionSandbox owner. They are diagnostic/reconciliation metadata;
	// the full container ID and daemon authority remain the resource identity.
	LabelGeneration  = "stella.sandbox.generation"
	LabelOwnerBootID = "stella.sandbox.owner_boot_id"
)
