package dockerclient

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestIsContainerStale(t *testing.T) {
	cases := []struct {
		name      string
		status    string
		ownerPID  string
		createdAt string
		want      bool
	}{
		{"exited is stale", "exited", "", "", true},
		{"dead is stale", "dead", "", "", true},
		{"created is stale", "created", "", "", true},
		{"running no pid not stale", "running", "", "", false},
		{"running bad pid not stale", "running", "notapid", "", false},
		{"paused no pid not stale", "paused", "", "", false},
		{"unknown no createdAt", "unknown", "", "", false},
		{"unknown old createdAt stale", "unknown", "", time.Now().Add(-2 * time.Hour).Format(time.RFC3339), true},
		{"unknown recent createdAt not stale", "unknown", "", time.Now().Add(-30 * time.Minute).Format(time.RFC3339), false},
		{"unknown bad createdAt not stale", "unknown", "", "bad-date", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isContainerStale(c.status, c.ownerPID, c.createdAt)
			if got != c.want {
				t.Fatalf("isContainerStale(%q, %q, %q) = %v, want %v", c.status, c.ownerPID, c.createdAt, got, c.want)
			}
		})
	}
}

func TestOwnerProcessGone(t *testing.T) {
	t.Run("empty pid", func(t *testing.T) {
		if ownerProcessGone("") {
			t.Fatal("empty pid should return false")
		}
	})
	t.Run("zero pid", func(t *testing.T) {
		if ownerProcessGone("0") {
			t.Fatal("pid=0 should return false")
		}
	})
	t.Run("negative pid", func(t *testing.T) {
		if ownerProcessGone("-1") {
			t.Fatal("negative pid should return false")
		}
	})
	t.Run("non-numeric", func(t *testing.T) {
		if ownerProcessGone("abc") {
			t.Fatal("non-numeric pid should return false")
		}
	})
	t.Run("current process is alive", func(t *testing.T) {
		pidStr := fmt.Sprintf("%d", os.Getpid())
		if ownerProcessGone(pidStr) {
			t.Fatal("current process should not be gone")
		}
	})
}
