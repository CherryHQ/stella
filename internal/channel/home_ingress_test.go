package channel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/home"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

type captureIngressWriter struct {
	key      home.Key
	filename string
	content  []byte
	err      error
}

func (w *captureIngressWriter) WriteInboundAsset(_ context.Context, key home.Key, filename string, content []byte) (string, error) {
	w.key, w.filename, w.content = key, filename, append([]byte(nil), content...)
	if w.err != nil {
		return "", w.err
	}
	return "$" + sandbox.EnvStellaAssetsDir + "/202608/asset", nil
}

type resolveServiceManager struct{}

func (resolveServiceManager) GetService(string) *agent.Service { return &agent.Service{} }
func (resolveServiceManager) Default() *agent.Service          { return &agent.Service{} }

type resolveGroup struct{ id string }

func (g resolveGroup) ResolveGroupID(context.Context, string, string, string) (string, error) {
	return g.id, nil
}

func TestCoordinatorSaveAssetUsesTypedPrincipalIngress(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	user := createTestUser(t, ts.oidcStore, "ingress@example.com")
	createTestIdentity(t, ts.oidcStore, user.ID, "telegram", "sender", "Ingress User")

	for _, tt := range []struct {
		name    string
		message pkgchannel.IncomingMessage
		groupID string
		want    home.Key
	}{
		{name: "DM", message: pkgchannel.IncomingMessage{Platform: "telegram", SenderID: "sender"}, want: home.Principal(home.UserPrincipal, user.ID)},
		{name: "group", message: pkgchannel.IncomingMessage{Platform: "telegram", SenderID: "sender", ChatID: "platform-group", IsGroup: true}, groupID: "canonical-group", want: home.Principal(home.GroupPrincipal, "canonical-group")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			writer := &captureIngressWriter{}
			coord := &Coordinator{serviceManager: resolveServiceManager{}, store: ts.store, auth: ts.oidcStore, agentAccess: agentaccess.NewService(ts.store, ts.authStore), ingressAssets: writer}
			if tt.groupID != "" {
				coord.groupResolver = resolveGroup{id: tt.groupID}
			}
			got, err := coord.SaveAsset(ctx, tt.message, "report.pdf", []byte("bytes"))
			if err != nil {
				t.Fatalf("SaveAsset: %v", err)
			}
			if got != "$"+sandbox.EnvStellaAssetsDir+"/202608/asset" || strings.Contains(got, "/tmp/") {
				t.Fatalf("portable path = %q", got)
			}
			if writer.key != tt.want || writer.filename != "report.pdf" || string(writer.content) != "bytes" {
				t.Fatalf("writer received key=%+v filename=%q bytes=%q", writer.key, writer.filename, writer.content)
			}
		})
	}
}

func TestCoordinatorSaveAssetFailsClosed(t *testing.T) {
	ts := setupStores(t)
	user := createTestUser(t, ts.oidcStore, "ingress-error@example.com")
	createTestIdentity(t, ts.oidcStore, user.ID, "telegram", "sender", "Ingress Error")
	want := errors.New("ingress unavailable")
	writer := &captureIngressWriter{err: want}
	coord := &Coordinator{serviceManager: resolveServiceManager{}, store: ts.store, auth: ts.oidcStore, agentAccess: agentaccess.NewService(ts.store, ts.authStore), ingressAssets: writer}
	if _, err := coord.SaveAsset(context.Background(), pkgchannel.IncomingMessage{Platform: "telegram", SenderID: "sender"}, "x", nil); !errors.Is(err, want) {
		t.Fatalf("writer error = %v, want %v", err, want)
	}
	if _, err := coord.SaveAsset(context.Background(), pkgchannel.IncomingMessage{Platform: "telegram", SenderID: "missing"}, "x", nil); err == nil {
		t.Fatal("unresolvable identity unexpectedly entered writer")
	}
	if _, err := (&Coordinator{}).SaveAsset(context.Background(), pkgchannel.IncomingMessage{}, "x", nil); err == nil {
		t.Fatal("missing ingress writer unexpectedly succeeded")
	}
}
