package memorytest

import "testing"

func TestFakeConformance(t *testing.T) {
	RunConformance(t, New())
}
