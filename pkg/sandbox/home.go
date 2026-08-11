package sandbox

// HomeAttachment identifies a workspace path relative to STELLA_HOME for
// sandbox mounting.
type HomeAttachment struct {
	HomeID   string
	Locator  string
	ReadOnly bool
}
