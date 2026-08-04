package sandbox

// HomeAttachment is an opaque, provider-compatible reference to persistent
// storage. Physical coordinates stay private to the HomeStore implementation.
type HomeAttachment struct {
	HomeID   string
	StoreID  string
	Locator  string
	ReadOnly bool
}
