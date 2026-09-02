package session

import (
	"slices"
	"strings"
)

// ReviewPolicy controls which sessions are included in reflect review.
type ReviewPolicy struct {
	// ExcludeKinds lists session kinds to skip.
	ExcludeKinds []Kind
}

// DefaultReviewPolicy returns the policy used by the reflect system.
// Delegate, task, and scheduler sessions are excluded by default.
func DefaultReviewPolicy() ReviewPolicy {
	return ReviewPolicy{
		ExcludeKinds: []Kind{KindDelegate, KindTask, KindScheduler},
	}
}

// IsZero reports whether no review policy was supplied.
func (p ReviewPolicy) IsZero() bool {
	return len(p.ExcludeKinds) == 0
}

// Includes reports whether a session with the given kind passes this policy.
func (p ReviewPolicy) Includes(kind Kind) bool {
	return !slices.Contains(p.ExcludeKinds, kind)
}

// isPrivateMainChannel reports whether channel encodes a per-user private channel.
// The convention is that private channels contain ":user:" in their key.
func isPrivateMainChannel(channel string, userID string) bool {
	return userID != "" && strings.Contains(channel, ":user:")
}

// isMainCandidate reports whether a session is eligible to become the main session.
func isMainCandidate(info Info, userID string) bool {
	if info.Archived || info.UserID != userID || info.ProjectID != "" {
		return false
	}
	if Kind(info.Kind) == KindTask || Kind(info.Kind) == KindScheduler {
		return false
	}
	return isPrivateMainChannel(info.Channel, userID)
}
