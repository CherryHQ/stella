package dockerclient

import "testing"

func TestIsContainerTerminal(t *testing.T) {
	cases := []struct {
		name   string
		status string
		want   bool
	}{
		{"exited is terminal", "exited", true},
		{"dead is terminal", "dead", true},
		{"created remains pending", "created", false},
		{"running is not terminal", "running", false},
		{"paused is not terminal", "paused", false},
		{"unknown is not terminal", "unknown", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isContainerTerminal(c.status)
			if got != c.want {
				t.Fatalf("isContainerTerminal(%q) = %v, want %v", c.status, got, c.want)
			}
		})
	}
}
