package observability

// ChannelName returns the historical low-cardinality channel label used by
// telemetry exporters. Business context keeps the canonical platform name.
func ChannelName(platform string) string {
	if platform == "weixin" {
		return "wechat"
	}
	return platform
}
