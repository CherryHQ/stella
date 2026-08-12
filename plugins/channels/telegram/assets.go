package telegram

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/pkg/channel"
)

// saveAsset delegates inbound attachment publication to the host-owned
// AssetSaver capability, which resolves identity and writes through an
// authorized Home root. A handler without the capability fails closed.
func (b *Bot) saveAsset(ctx context.Context, msg channel.IncomingMessage, fileName string, data []byte) (string, error) {
	saver, ok := b.handler.(channel.AssetSaver)
	if !ok {
		return "", fmt.Errorf("asset storage unavailable")
	}
	return saver.SaveAsset(ctx, msg, fileName, data)
}
