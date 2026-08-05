package access

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestReadArtifactUsesExactSessionFilesystemAndSemanticAliases(t *testing.T) {
	svc, runtime, _, authority := newRuntimeTestService(t)
	filesystem := newWorkspaceFakeFilesystem()
	filesystem.files["/workspace/report.html"] = []byte("agent")
	filesystem.files["/user/assets/202607/report.html"] = []byte("user")
	runtime.filesystem = filesystem
	access, err := svc.Begin(context.Background(), authority)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path  string
		scope WorkspaceScope
		want  string
	}{
		{"report.html", WorkspaceScopeAgent, "agent"},
		{"$HOME/report.html", WorkspaceScopeUser, "agent"},
		{"/workspace/report.html", WorkspaceScopeUser, "agent"},
		{"$STELLA_ASSETS_DIR/202607/report.html", WorkspaceScopeAgent, "user"},
		{"/user/assets/202607/report.html", WorkspaceScopeAgent, "user"},
	} {
		before := runtime.filesystemCalls
		got, err := access.ReadArtifact(context.Background(), ArtifactReadInput{AgentID: "a1", SessionID: "s1", Scope: tc.scope, Path: tc.path, MaxBytes: 1024})
		if err != nil {
			t.Fatalf("ReadArtifact(%q): %v", tc.path, err)
		}
		if string(got.Content) != tc.want || got.Info.ID != "s1" || got.Info.AgentID != "a1" || got.Info.UserID != string(authority.UserID()) {
			t.Fatalf("ReadArtifact(%q) = %+v content=%q", tc.path, got.Info, got.Content)
		}
		if runtime.filesystemCalls != before+1 || runtime.filesystemInfo.ID != "s1" {
			t.Fatalf("callback calls=%d info=%+v, want one exact lease", runtime.filesystemCalls-before, runtime.filesystemInfo)
		}
	}
	agentAuthority, err := authz.NewAgentAuthority(authority.UserID(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	agentAccess, err := svc.Begin(context.Background(), agentAuthority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentAccess.ReadArtifact(context.Background(), ArtifactReadInput{AgentID: "a1", SessionID: "s1", Scope: WorkspaceScopeAgent, Path: "report.html", MaxBytes: 1024}); err != nil {
		t.Fatalf("bound agent ReadArtifact: %v", err)
	}
}

func TestReadArtifactRejectsUnauthorizedOrUnsafeBeforeCallback(t *testing.T) {
	svc, runtime, _, authority := newRuntimeTestService(t)
	runtime.filesystem = newWorkspaceFakeFilesystem()
	cases := []struct {
		name                 string
		authority            authz.Authority
		agent, session, path string
		want                 error
	}{
		{"foreign user", mustUserAuthority(t, "foreign"), "a1", "s1", "x.html", ErrForbidden},
		{"admin cross-user", mustAdminAuthority(t, "admin"), "a1", "s1", "x.html", ErrForbidden},
		{"foreign agent", mustAgentAuthority(t, authority.UserID(), "other"), "a1", "s1", "x.html", ErrForbidden},
		{"wrong route agent", authority, "other", "s1", "x.html", ErrForbidden},
		{"missing session", authority, "a1", "missing", "x.html", ErrNotFound},
		{"group actor", mustGroupAuthority(t), "a1", "s1", "x.html", ErrForbidden},
		{"system actor", mustSystemAuthority(t), "a1", "s1", "x.html", ErrForbidden},
		{"malformed alias", authority, "a1", "s1", "$STELLA_ASSETS_DIRx/a.html", ErrInvalid},
		{"traversal", authority, "a1", "s1", "../a.html", ErrInvalid},
		{"host coordinate", authority, "a1", "s1", "/private/stella/a.html", ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			access, err := svc.Begin(context.Background(), tc.authority)
			if err != nil {
				t.Fatal(err)
			}
			before := runtime.filesystemCalls
			_, err = access.ReadArtifact(context.Background(), ArtifactReadInput{AgentID: tc.agent, SessionID: tc.session, Scope: WorkspaceScopeAgent, Path: tc.path, MaxBytes: 1024})
			if !errors.Is(err, tc.want) {
				t.Fatalf("ReadArtifact error=%v, want %v", err, tc.want)
			}
			if runtime.filesystemCalls != before {
				t.Fatalf("unsafe/unauthorized read invoked callback")
			}
		})
	}
}

func TestReadFilesystemRawHardening(t *testing.T) {
	closeFailure := errors.New("close")
	for _, tc := range []struct {
		name     string
		data     []byte
		info     pkgsandbox.FileInfo
		max      int64
		err      error
		closeErr error
		want     error
	}{
		{"directory", []byte("x"), pkgsandbox.FileInfo{IsDir: true, Mode: fs.ModeDir}, 1, nil, nil, ErrIsDir},
		{"nonregular", []byte("x"), pkgsandbox.FileInfo{Mode: fs.ModeNamedPipe, Size: 1}, 1, nil, nil, ErrInvalid},
		{"negative size", []byte("x"), pkgsandbox.FileInfo{Mode: 0o644, Size: -1}, 1, nil, nil, ErrInvalid},
		{"declared over max", []byte("x"), pkgsandbox.FileInfo{Mode: 0o644, Size: 2}, 1, nil, nil, ErrTooLarge},
		{"provider limit", []byte("xx"), pkgsandbox.FileInfo{Mode: 0o644, Size: 2}, 1, nil, nil, ErrTooLarge},
		{"short metadata", []byte("x"), pkgsandbox.FileInfo{Mode: 0o644, Size: 2}, 3, nil, nil, ErrInvalid},
		{"close failure", []byte("x"), pkgsandbox.FileInfo{Mode: 0o644, Size: 1}, 1, nil, closeFailure, closeFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filesystem := newWorkspaceFakeFilesystem()
			filesystem.files["/workspace/a.html"] = tc.data
			info := tc.info
			filesystem.readInfo = &info
			filesystem.readCloseErr = tc.closeErr
			_, _, err := readFilesystemRaw(context.Background(), filesystem, "/workspace/a.html", tc.max)
			if !errors.Is(err, tc.want) {
				t.Fatalf("readFilesystemRaw error=%v, want %v", err, tc.want)
			}
		})
	}
	// A faulty provider can ignore ReadOptions.MaxBytes; the local max+1 reader
	// must still distinguish it from an exact-limit payload without allocating it.
	filesystem := newWorkspaceFakeFilesystem()
	filesystem.ignoreReadLimit = true
	filesystem.files["/workspace/long.html"] = []byte("xx")
	info := pkgsandbox.FileInfo{Mode: 0o644, Size: 1}
	filesystem.readInfo = &info
	if _, _, err := readFilesystemRaw(context.Background(), filesystem, "/workspace/long.html", 1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("local limit error=%v, want ErrTooLarge", err)
	}
}

func mustUserAuthority(t *testing.T, id string) authz.Authority {
	t.Helper()
	a, err := authz.NewUserAuthority(authz.UserID(id), false)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustAdminAuthority(t *testing.T, id string) authz.Authority {
	t.Helper()
	a, err := authz.NewUserAuthority(authz.UserID(id), true)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustAgentAuthority(t *testing.T, user authz.UserID, agent string) authz.Authority {
	t.Helper()
	a, err := authz.NewAgentAuthority(user, authz.AgentID(agent))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustGroupAuthority(t *testing.T) authz.Authority {
	t.Helper()
	a, err := authz.NewGroupAgentAuthority("group", "a1")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustSystemAuthority(t *testing.T) authz.Authority {
	t.Helper()
	a, err := authz.NewSystemAuthority("test")
	if err != nil {
		t.Fatal(err)
	}
	return a
}
