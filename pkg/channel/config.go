package channel

const (
	DefaultGuestMessageLimitPerMinute = 10
	DefaultGuestMaxPerChannel         = 1000
	DefaultGuestRetentionDays         = 30
	MaxGuestMessageLimitPerMinute     = 120
	MaxGuestMaxPerChannel             = 100000
	MaxGuestRetentionDays             = 365
)

// GuestConfig is the platform-independent subset controlling restricted guest
// admission and resource bounds.
type GuestConfig struct {
	AllowDM                    bool
	AllowUnlinkedDM            bool
	GuestMessageLimitPerMinute int
	GuestMaxPerChannel         int
	GuestRetentionDays         int
}

// GuestPolicyDecoder decodes a persisted plugin configuration into the shared
// guest admission policy. The complete plugin decoder must run first so the
// policy keeps the plugin's defaults and validation semantics.
type GuestPolicyDecoder func(rawConfig string) (GuestConfig, error)

// GuestPolicyResolver selects the plugin-owned decoder by persisted channel
// type. Unknown types must return an error so callers fail closed.
type GuestPolicyResolver func(channelType, rawConfig string) (GuestConfig, error)
