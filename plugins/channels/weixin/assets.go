package weixin

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/pkg/channel"
)

// saveAsset persists an inbound attachment through the host's authoritative asset
// store (the AssetSaver capability on the coordinator handler), replacing the old
// blob process-global. A handler without the capability fails closed rather than
// writing a local-only file that would not survive across replicas.
func (b *Bot) saveAsset(ctx context.Context, assetsDir, fileName string, data []byte) (string, error) {
	saver, ok := b.handler.(channel.AssetSaver)
	if !ok {
		return "", fmt.Errorf("asset storage unavailable")
	}
	return saver.SaveAsset(ctx, assetsDir, fileName, data)
}
