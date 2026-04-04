package channel

var builtinChannelNames = []string{"telegram", "qq", "feishu", "weixin"}

func BuiltinChannelNames() []string {
	names := make([]string, len(builtinChannelNames))
	copy(names, builtinChannelNames)
	return names
}
