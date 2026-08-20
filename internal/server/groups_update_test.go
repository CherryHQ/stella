package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/auth"
)

func patchGroup(t *testing.T, s *Server, userID, groupID string, req apitypes.UpdateGroupRequest) *httptest.ResponseRecorder {
	t.Helper()
	buf, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	r := httptest.NewRequest(http.MethodPatch, "/api/groups/"+groupID, bytes.NewReader(buf))
	r = r.WithContext(withAuthInfo(r.Context(), &AuthInfo{UserID: userID, Role: auth.RoleUser}))
	rr := httptest.NewRecorder()
	s.UpdateGroup(rr, r, groupID)
	return rr
}

func decodeGroup(t *testing.T, rr *httptest.ResponseRecorder) apitypes.Group {
	t.Helper()
	var g apitypes.Group
	if err := json.Unmarshal(rr.Body.Bytes(), &g); err != nil {
		t.Fatalf("decode group: %v (body %q)", err, rr.Body.String())
	}
	return g
}

// PATCH carries independent fields: a request with both a rename and a cap must
// apply both, not pick one.
func TestUpdateGroupAppliesNameAndCapsTogether(t *testing.T) {
	s, _, userID, groupID := setupGroupSSE(t)
	name := "renamed"
	hold := 7
	rr := patchGroup(t, s, userID, groupID, apitypes.UpdateGroupRequest{GroupName: &name, HoldLimit: &hold})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rr.Code, rr.Body.String())
	}
	got := decodeGroup(t, rr)
	if got.GroupName != name {
		t.Fatalf("group_name = %q, want %q", got.GroupName, name)
	}
	if got.HoldLimit == nil || *got.HoldLimit != hold {
		t.Fatalf("hold_limit = %v, want %d", got.HoldLimit, hold)
	}
}

// An out-of-range cap must say which cap, and must not be reported as a
// pagination problem by a request that never paginated anything.
func TestUpdateGroupRejectsOutOfRangeCapByName(t *testing.T) {
	s, _, userID, groupID := setupGroupSSE(t)
	hold := 99
	rr := patchGroup(t, s, userID, groupID, apitypes.UpdateGroupRequest{HoldLimit: &hold})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %q)", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, "hold_limit") || strings.Contains(body, "pagination") {
		t.Fatalf("error body = %q, want it to name hold_limit and not mention pagination", body)
	}
}

func TestUpdateGroupRequiresAField(t *testing.T) {
	s, _, userID, groupID := setupGroupSSE(t)
	rr := patchGroup(t, s, userID, groupID, apitypes.UpdateGroupRequest{})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %q)", rr.Code, rr.Body.String())
	}
}
