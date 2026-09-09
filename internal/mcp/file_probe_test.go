package mcp

import (
	"context"
	"errors"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/plugin"
)

func TestProbeFileProjectsHealthyCatalogWithoutPersistence(t *testing.T) {
	reg, authority := probeFileRegistration(t)
	client := &fakeMCPClient{tools: []*mcpsdk.Tool{{Name: "echo", Description: "echoes input"}}}
	svc := probeFileService(client, nil)

	got, err := svc.ProbeFile(t.Context(), reg, authority)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusOK || len(got.Tools) != 1 || got.Tools[0].Name != "echo" {
		t.Fatalf("probe = status %q tools %#v, want healthy echo catalog", got.Status, got.Tools)
	}
	if got.ConfigRevision != 0 {
		t.Fatalf("file probe invented common config revision %d", got.ConfigRevision)
	}
}

func TestProbeFileMapsUnreachableToSafeErrorObservation(t *testing.T) {
	reg, authority := probeFileRegistration(t)
	svc := probeFileService(nil, errors.New("dial timeout"))

	got, err := svc.ProbeFile(t.Context(), reg, authority)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusError || got.StatusError != probeFailedHint || len(got.Tools) != 0 {
		t.Fatalf("probe = %#v, want safe error observation", got)
	}
}

func TestProbeFileMapsAuthFailureToNeedsAuth(t *testing.T) {
	reg, authority := probeFileRegistration(t)
	svc := probeFileService(nil, errors.New("MCP tools/list: Unauthorized"))

	got, err := svc.ProbeFile(t.Context(), reg, authority)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusNeedsAuth || got.StatusError != credentialRejectedHint {
		t.Fatalf("probe = %#v, want needs_auth observation", got)
	}
}

func probeFileRegistration(t *testing.T) (Registration, authz.Authority) {
	t.Helper()
	userID := authz.UserID("00000000-0000-0000-0000-000000000001")
	authority, err := authz.NewUserAuthority(userID, false)
	if err != nil {
		t.Fatal(err)
	}
	return Registration{
		IdentityKind: RegistrationIdentityFile,
		FileKey:      plugin.ResourceKey{Scope: plugin.ScopeUser, UserID: string(userID), Kind: plugin.ResourceMCP, Name: "probe"},
		ID:           "file-probe-registration", Scope: ScopeUser, UserID: string(userID), Name: "probe",
		URL: "https://mcp.example.test", Transport: TransportStreamableHTTP,
		AuthType: AuthTypeNone, CredentialMode: CredentialModePerUser,
		AuthenticationTarget: "probe", ServerKey: "probe",
	}, authority
}

func probeFileService(client *fakeMCPClient, connectErr error) *Service {
	svc := NewService(nil, nil)
	svc.fileSessionFactory = func(s *Service) *FileSession {
		session := NewFileSession(s)
		session.SetConnectForTesting(func(context.Context, Registration, CredentialOwner, func()) (RemoteClient, error) {
			if connectErr != nil {
				return nil, connectErr
			}
			return client, nil
		})
		return session
	}
	return svc
}
