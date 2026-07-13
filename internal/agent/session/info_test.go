package session

import (
	"reflect"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
)

// a canonical (parseable) group id used across the group-scope tests.
const testGroupID = "11111111-1111-4111-8111-111111111111"

// TestInfoIsNotMemoryAlias guards the architecture invariant that session.Info
// is a distinct session-domain type, not an alias to the memory persistence
// type. If Info were reintroduced as `type Info = memory.SessionInfo`, the two
// reflect types would be identical (and Info.MemoryScope/Record could not be
// declared on it at all).
func TestInfoIsNotMemoryAlias(t *testing.T) {
	if reflect.TypeFor[Info]() == reflect.TypeFor[memory.SessionInfo]() {
		t.Fatal("session.Info must not be an alias of memory.SessionInfo")
	}
}

// TestMemoryScope_Kinds covers every known session kind and scope so read,
// write, and compaction always derive one partition per session from the single
// canonical conversion.
//
// Note on group scope: production group sessions are owned by the group, so
// UserID == GroupID and the conversation partition (keyed on session/user/agent)
// already lands on the group id. There is no reachable double-partition bug in
// the current tree; the value of a single conversion plus a durable, validated
// GroupID is that the invariant is enforced rather than left implicit.
func TestMemoryScope_Kinds(t *testing.T) {
	const (
		agentID = "agent"
		userID  = "user-1"
	)
	cases := []struct {
		name string
		info Info
		want memory.Session
	}{
		{
			name: "main private",
			info: Info{ID: "agent:user:user-1:private", AgentID: agentID, UserID: userID, Channel: "web", Kind: string(KindMain)},
			want: memory.Session{ID: "agent:user:user-1:private", AgentID: agentID, UserID: userID, Channel: "web"},
		},
		{
			name: "chat private",
			info: Info{ID: "s-chat", AgentID: agentID, UserID: userID, Channel: string(ChannelWeb), Kind: string(KindChat)},
			want: memory.Session{ID: "s-chat", AgentID: agentID, UserID: userID, Channel: "web"},
		},
		{
			name: "task",
			info: Info{ID: "s-task", AgentID: agentID, UserID: userID, Channel: string(ChannelTask), Kind: string(KindTask)},
			want: memory.Session{ID: "s-task", AgentID: agentID, UserID: userID, Channel: "task"},
		},
		{
			name: "delegate",
			info: Info{ID: "s-del", AgentID: agentID, UserID: userID, Channel: string(ChannelDelegate), Kind: string(KindDelegate)},
			want: memory.Session{ID: "s-del", AgentID: agentID, UserID: userID, Channel: "delegate"},
		},
		{
			name: "scheduler",
			info: Info{ID: "s-sch", AgentID: agentID, UserID: userID, Channel: string(ChannelScheduler), Kind: string(KindScheduler)},
			want: memory.Session{ID: "s-sch", AgentID: agentID, UserID: userID, Channel: "scheduler"},
		},
		{
			name: "project scoped",
			info: Info{ID: "s-proj", AgentID: agentID, UserID: userID, Channel: string(ChannelWeb), Kind: string(KindChat), ProjectID: "proj-1"},
			// ProjectID scopes memory via context, not the partition key.
			want: memory.Session{ID: "s-proj", AgentID: agentID, UserID: userID, Channel: "web"},
		},
		{
			name: "channel telegram",
			info: Info{ID: "s-tg", AgentID: agentID, UserID: userID, Channel: string(ChannelTelegram), Kind: string(KindChat)},
			want: memory.Session{ID: "s-tg", AgentID: agentID, UserID: userID, Channel: "telegram"},
		},
		{
			name: "group (owned by the group: UserID == GroupID)",
			info: Info{ID: "agent:group:" + testGroupID, AgentID: agentID, UserID: testGroupID, GroupID: testGroupID, Channel: "group:" + testGroupID, Kind: string(KindChat)},
			want: memory.Session{ID: "agent:group:" + testGroupID, AgentID: agentID, UserID: testGroupID, Channel: "group:" + testGroupID, GroupID: testGroupID},
		},
	}

	reg := &Registry{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.info.MemoryScope()
			if err != nil {
				t.Fatalf("Info.MemoryScope: unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Info.MemoryScope:\n got  %+v\n want %+v", got, tc.want)
			}
			// Registry.MemoryScope must not diverge from the canonical conversion.
			regGot, err := reg.MemoryScope(tc.info)
			if err != nil {
				t.Fatalf("Registry.MemoryScope: unexpected error: %v", err)
			}
			if regGot != tc.want {
				t.Fatalf("Registry.MemoryScope:\n got  %+v\n want %+v", regGot, tc.want)
			}
		})
	}
}

// TestMemoryScope_FailsClosed proves the scope conversion refuses an Info that
// violates the session invariant rather than emit a half-formed scope.
func TestMemoryScope_FailsClosed(t *testing.T) {
	bad := Info{ID: "s", AgentID: "a", UserID: "u", GroupID: testGroupID} // UserID != GroupID
	if _, err := bad.MemoryScope(); err == nil {
		t.Fatal("MemoryScope must fail closed on an invalid group Info")
	}
}

// TestInfoValidate exercises the durable session-scope invariant: UserID is
// always required, and a group session must be owned by its group (UserID ==
// GroupID) with a canonical group id.
func TestInfoValidate(t *testing.T) {
	cases := []struct {
		name    string
		info    Info
		wantErr bool
	}{
		{name: "valid private", info: Info{ID: "s", AgentID: "a", UserID: "u"}},
		{name: "valid group", info: Info{ID: "s", AgentID: "a", UserID: testGroupID, GroupID: testGroupID}},
		{name: "missing id", info: Info{AgentID: "a", UserID: "u"}, wantErr: true},
		{name: "missing agent", info: Info{ID: "s", UserID: "u"}, wantErr: true},
		{name: "missing user", info: Info{ID: "s", AgentID: "a"}, wantErr: true},
		{name: "group user must equal group", info: Info{ID: "s", AgentID: "a", UserID: "u", GroupID: testGroupID}, wantErr: true},
		{name: "group id not a uuid", info: Info{ID: "s", AgentID: "a", UserID: "not-a-uuid", GroupID: "not-a-uuid"}, wantErr: true},
		// uuid.Parse accepts these non-canonical forms, but the durable group id
		// must be the exact canonical lowercase-hyphenated UUID.
		{name: "group id uppercase", info: Info{ID: "s", AgentID: "a", UserID: "11111111-1111-4111-8111-111111111111", GroupID: "11111111-1111-4111-8111-111111111111"}, wantErr: false},
		{name: "group id uppercased rejected", info: Info{ID: "s", AgentID: "a", UserID: "11111111-1111-4111-8111-11111111111A", GroupID: "11111111-1111-4111-8111-11111111111A"}, wantErr: true},
		{name: "group id braces rejected", info: Info{ID: "s", AgentID: "a", UserID: "{11111111-1111-4111-8111-111111111111}", GroupID: "{11111111-1111-4111-8111-111111111111}"}, wantErr: true},
		{name: "group id hyphenless rejected", info: Info{ID: "s", AgentID: "a", UserID: "11111111111141118111111111111111", GroupID: "11111111111141118111111111111111"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.info.Validate(); (err != nil) != tc.wantErr {
				t.Fatalf("Validate() err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestInfoRecordRoundTrip verifies the persistence boundary mapping is lossless
// for both private and group sessions.
func TestInfoRecordRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cases := []Info{
		{ID: "s-1", AgentID: "agent", UserID: "user-1", Channel: "web", Kind: string(KindChat), ProjectID: "proj-1", Title: "hello", CreatedAt: now, LastActive: now, Archived: true},
		{ID: "agent:group:" + testGroupID, AgentID: "agent", UserID: testGroupID, GroupID: testGroupID, Channel: "group:" + testGroupID, Kind: string(KindChat), CreatedAt: now, LastActive: now},
	}
	for _, info := range cases {
		rec, err := info.Record()
		if err != nil {
			t.Fatalf("Record(%q): %v", info.ID, err)
		}
		got, err := InfoFromRecord(rec)
		if err != nil {
			t.Fatalf("InfoFromRecord(%q): %v", info.ID, err)
		}
		if got != info {
			t.Fatalf("round trip mismatch:\n got  %+v\n want %+v", got, info)
		}
	}
}

// TestInfoFromRecord_FailsClosed proves the persistence→domain boundary rejects
// a record that violates the session invariant.
func TestInfoFromRecord_FailsClosed(t *testing.T) {
	cases := []struct {
		name string
		rec  memory.SessionInfo
	}{
		{name: "missing user", rec: memory.SessionInfo{ID: "s", AgentID: "a"}},
		{name: "group user mismatch", rec: memory.SessionInfo{ID: "s", AgentID: "a", UserID: "u", GroupID: testGroupID}},
		{name: "group id not canonical", rec: memory.SessionInfo{ID: "s", AgentID: "a", UserID: "g", GroupID: "g"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := InfoFromRecord(tc.rec); err == nil {
				t.Fatalf("InfoFromRecord must fail closed on %s", tc.name)
			}
		})
	}
}

// TestRecord_FailsClosed proves the domain→persistence boundary rejects an
// invalid Info instead of persisting it.
func TestRecord_FailsClosed(t *testing.T) {
	if _, err := (Info{ID: "s", AgentID: "a"}).Record(); err == nil {
		t.Fatal("Record must fail closed on an Info missing a user owner")
	}
}
