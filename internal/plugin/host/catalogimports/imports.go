// Package catalogimports registers every plugin included in Stella's default
// catalog. Commands and build generators import it so they enumerate the same
// bundled skill synchronizers as the daemon.
package catalogimports

import (
	_ "github.com/CherryHQ/stella/internal/reflect"
	_ "github.com/CherryHQ/stella/plugins/channels/dingtalk"
	_ "github.com/CherryHQ/stella/plugins/channels/discord"
	_ "github.com/CherryHQ/stella/plugins/channels/feishu"
	_ "github.com/CherryHQ/stella/plugins/channels/qq"
	_ "github.com/CherryHQ/stella/plugins/channels/telegram"
	_ "github.com/CherryHQ/stella/plugins/channels/weixin"
)
