package telegram

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

type fakeChannelHandler struct{}

func (fakeChannelHandler) HandleIncoming(context.Context, pkgchannel.IncomingMessage, string, string) (string, bool, *pkgchannel.ChatStream, error) {
	return "", false, nil, nil
}
func (fakeChannelHandler) ListModels() []pkgchannel.ModelOption { return nil }
func (fakeChannelHandler) SwitchModel(string, string) error     { return nil }
func (fakeChannelHandler) ListAgents(context.Context, pkgchannel.IncomingMessage) ([]pkgchannel.AgentInfo, string, error) {
	return nil, "", nil
}

func (fakeChannelHandler) SwitchAgent(context.Context, pkgchannel.IncomingMessage, string) error {
	return nil
}

type savedTestAsset struct {
	assetsDir string
	fileName  string
	data      []byte
}

// assetChannelHandler adds the optional storage capabilities exercised by
// Telegram's inbound image and document paths.
type assetChannelHandler struct {
	fakeChannelHandler
	userRoot  string
	saveErr   error
	saveCalls []savedTestAsset
}

func (h *assetChannelHandler) ResolveUserRoot(context.Context, pkgchannel.IncomingMessage) (string, error) {
	return h.userRoot, nil
}

func (h *assetChannelHandler) SaveAsset(_ context.Context, assetsDir, fileName string, data []byte) (string, error) {
	if h.saveErr != nil {
		return "", h.saveErr
	}
	h.saveCalls = append(h.saveCalls, savedTestAsset{
		assetsDir: assetsDir,
		fileName:  fileName,
		data:      append([]byte(nil), data...),
	})
	return filepath.Join(assetsDir, fileName), nil
}

func waitClosed(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
	}
}
