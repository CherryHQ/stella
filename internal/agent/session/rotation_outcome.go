package session

import "context"

// RotationOutcome is what a re-read of the chat's session binding can say about
// a rotation whose commit acknowledgement was lost
// (ErrRotationOutcomeUnknown): the transaction may have landed, so the error
// alone is not an answer. The binding is the answer — it is what the rotation
// moves, and it never moves back.
type RotationOutcome int

const (
	// RotationUncertain means the verification read itself failed, so nothing is
	// known. A caller must say so rather than invite a retry: retrying a
	// rotation that did commit archives the fresh context instead of the old.
	RotationUncertain RotationOutcome = iota
	// RotationCommitted means the binding has left the expected session, so the
	// rotation did happen and should be reported as success.
	RotationCommitted
	// RotationNotExecuted means the binding still points at the expected
	// session, so the transaction rolled back. Retrying is safe.
	RotationNotExecuted
)

// VerifyRotation classifies a lost-acknowledgement rotation by re-resolving the
// chat's session binding. expectedSessionID is the session the rotation was
// asked to replace; resolve reads the binding's current session.
//
// A resolve that succeeds but names nothing is treated as uncertain: an empty
// id is not evidence that the binding moved.
func VerifyRotation(ctx context.Context, expectedSessionID string, resolve func(context.Context) (Info, error)) RotationOutcome {
	if expectedSessionID == "" || resolve == nil {
		return RotationUncertain
	}
	info, err := resolve(ctx)
	if err != nil || info.ID == "" {
		return RotationUncertain
	}
	if info.ID == expectedSessionID {
		return RotationNotExecuted
	}
	return RotationCommitted
}
