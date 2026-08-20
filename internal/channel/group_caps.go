package channel

// groupReasonHardCap is the single name both hard-cap call sites report, so a
// silenced turn reads the same in triage logs and in dispatch rows.
const groupReasonHardCap = "hard_cap"

// groupCapCheck pairs one measured count with the ceiling it must stay under.
type groupCapCheck struct {
	count int64
	limit int64
}

// exceedsGroupHardCap is the one definition of the group hard cap. The rule is
// deliberately evaluated twice: once unlocked before a turn starts, where all
// three ceilings are cheap to read and a blocked wake costs nothing, and once
// under the group state row lock after the turn, where only the reply count is
// re-read because the lock is what makes it authoritative against a peer that
// committed in the meantime. Both call sites pass what they themselves
// measured; sharing the comparison is what stops the two copies drifting apart
// while still leaving each side free to measure less.
func exceedsGroupHardCap(checks ...groupCapCheck) (exceeded bool, reason string) {
	for _, check := range checks {
		if check.count >= check.limit {
			return true, groupReasonHardCap
		}
	}
	return false, ""
}
