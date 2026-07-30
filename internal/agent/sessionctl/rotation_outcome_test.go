package sessionctl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
)

// lostAckAccess reports a rotation whose commit acknowledgement was lost. It can
// let the rotation actually land first (committed but unacknowledged) or not
// (rolled back), and it can make the follow-up verification read fail — the
// three shapes confirm_new must tell apart.
type lostAckAccess struct {
	registryAccess
	commit     bool
	resolveErr error
}

func (a lostAckAccess) Begin(context.Context, authz.Authority) (agent.SessionAccess, error) {
	return a, nil
}

func (a lostAckAccess) lostAck() error {
	return fmt.Errorf("connection reset before response: %w", session.ErrRotationOutcomeUnknown)
}

func (a lostAckAccess) RotateMain(ctx context.Context, userID, agentID, expectedSessionID string) (session.Info, error) {
	if a.commit {
		if _, err := a.registryAccess.RotateMain(ctx, userID, agentID, expectedSessionID); err != nil {
			return session.Info{}, err
		}
	}
	return session.Info{}, a.lostAck()
}

func (a lostAckAccess) ResolveMain(ctx context.Context, userID, agentID string) (session.Info, error) {
	if a.resolveErr != nil {
		return session.Info{}, a.resolveErr
	}
	return a.registryAccess.ResolveMain(ctx, userID, agentID)
}

// confirmWithLostAck runs the two-phase reset against the given lost-ack access
// and returns what confirm_new reported.
func confirmWithLostAck(t *testing.T, access lostAckAccess) (string, error) {
	t.Helper()
	f := newFixture(t)
	askCtx, before := f.dmTurn(t, newTurnID())
	nonce := requestNew(t, f, askCtx)

	access.registryAccess = registryAccess{reg: f.svc.Sessions}
	f.svc.SessionAccess = access

	return confirmNew(f, dmTurnCtx(before.ID, newTurnID()), nonce)
}

// TestConfirmLostAckReportsSuccessWhenBindingMoved covers the outcome the plain
// failure path got wrong: the COMMIT landed and only the acknowledgement was
// lost, so the chat IS on a fresh session. Reporting failure would push the user
// into a retry that rotates a second time and archives the fresh context.
func TestConfirmLostAckReportsSuccessWhenBindingMoved(t *testing.T) {
	reply, err := confirmWithLostAck(t, lostAckAccess{commit: true})
	if err != nil {
		t.Fatalf("a committed rotation must be reported as success, got error: %v", err)
	}
	if !strings.Contains(reply, "NEXT message") {
		t.Fatalf("reply = %q, want the normal success text", reply)
	}
}

// TestConfirmLostAckReportsNotExecutedWhenBindingUnchanged is the other half:
// the binding never moved, which proves the transaction rolled back. The user
// may safely ask again.
func TestConfirmLostAckReportsNotExecutedWhenBindingUnchanged(t *testing.T) {
	reply, err := confirmWithLostAck(t, lostAckAccess{commit: false})
	if err == nil {
		t.Fatalf("a rolled-back rotation must be reported as a failure, got reply %q", reply)
	}
	if !errors.Is(err, errRotationNotExecuted) {
		t.Fatalf("err = %v, want the not-executed report", err)
	}
	if !strings.Contains(err.Error(), "nothing was reset") {
		t.Fatalf("err = %v, want it to state that nothing was reset", err)
	}
}

// TestConfirmLostAckReportsUncertainWhenVerifyFails covers the case nothing can
// answer: the commit acknowledgement was lost AND the binding could not be read
// back. The report must not invite a retry, because a retry after a reset that
// did land throws away the fresh context.
func TestConfirmLostAckReportsUncertainWhenVerifyFails(t *testing.T) {
	reply, err := confirmWithLostAck(t, lostAckAccess{
		commit:     true,
		resolveErr: errors.New("db unreachable"),
	})
	if err == nil {
		t.Fatalf("an unverifiable rotation must not be reported as success, got reply %q", reply)
	}
	if !errors.Is(err, errRotationUncertain) {
		t.Fatalf("err = %v, want the uncertain report", err)
	}
	if !strings.Contains(err.Error(), "Do NOT retry") {
		t.Fatalf("err = %v, want it to warn against a blind retry", err)
	}
}
