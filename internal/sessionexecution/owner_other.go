//go:build !unix && !windows

package sessionexecution

// pidAlive conservatively reports alive: an uncheckable writer must fail
// closed rather than permit a blind takeover.
func pidAlive(int) bool { return true }
