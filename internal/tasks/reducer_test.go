package tasks

import (
	"testing"
)

func TestValidateTaskTransition_Goal(t *testing.T) {
	tests := []struct {
		from, to string
		role     Role
		wantErr  bool
	}{
		{"draft", "ready", RoleManager, false},
		{"draft", "cancelled", RoleManager, false},
		{"draft", "running", RoleManager, true},
		{"ready", "running", RoleSystem, false},
		{"ready", "done", RoleManager, false},
		{"running", "done", RoleManager, false},
		{"running", "failed", RoleManager, false},
		{"running", "blocked", RoleManager, false},
		{"blocked", "ready", RoleUser, false},
		{"done", "ready", RoleManager, false},     // reopen
		{"failed", "ready", RoleManager, false},   // reopen
		{"done", "failed", RoleManager, true},     // not allowed
		{"cancelled", "ready", RoleManager, true}, // terminal
	}
	for _, tt := range tests {
		err := ValidateTaskTransition("goal", tt.from, tt.to, tt.role)
		if (err != nil) != tt.wantErr {
			t.Errorf("goal %q→%q (role=%s): got err=%v, wantErr=%v", tt.from, tt.to, tt.role, err, tt.wantErr)
		}
	}
}

func TestValidateTaskTransition_Task(t *testing.T) {
	tests := []struct {
		from, to string
		role     Role
		wantErr  bool
	}{
		{"draft", "ready", RoleManager, false},
		{"ready", "running", RoleSystem, false},
		{"running", "reviewing", RoleWorker, false},
		{"running", "blocked", RoleWorker, false},
		{"running", "failed", RoleWorker, false},
		{"running", "done", RoleWorker, true}, // worker cannot mark done
		{"reviewing", "done", RoleReviewer, false},
		{"reviewing", "changes_requested", RoleReviewer, false},
		{"reviewing", "failed", RoleReviewer, false},
		{"reviewing", "ready", RoleReviewer, true}, // not an allowed transition
		{"changes_requested", "ready", RoleSystem, false},
		{"done", "ready", RoleManager, false}, // reopen
	}
	for _, tt := range tests {
		err := ValidateTaskTransition("task", tt.from, tt.to, tt.role)
		if (err != nil) != tt.wantErr {
			t.Errorf("task %q→%q (role=%s): got err=%v, wantErr=%v", tt.from, tt.to, tt.role, err, tt.wantErr)
		}
	}
}

func TestValidateTaskTransition_RoleRestrictions(t *testing.T) {
	// Worker can only do running→reviewing, running→blocked, running→failed
	if err := ValidateTaskTransition("task", "ready", "running", RoleWorker); err == nil {
		t.Error("worker should not be able to trigger ready→running")
	}
	if err := ValidateTaskTransition("task", "reviewing", "done", RoleWorker); err == nil {
		t.Error("worker should not be able to trigger reviewing→done")
	}

	// Reviewer can only do reviewing→{done,changes_requested,failed}
	if err := ValidateTaskTransition("task", "running", "reviewing", RoleReviewer); err == nil {
		t.Error("reviewer should not be able to trigger running→reviewing")
	}
}

func TestValidateRunTransition(t *testing.T) {
	tests := []struct {
		from, to string
		wantErr  bool
	}{
		{"queued", "running", false},
		{"queued", "cancelled", false},
		{"running", "completed", false},
		{"running", "failed", false},
		{"running", "cancelled", false},
		{"running", "interrupted", false},
		{"completed", "running", true},   // terminal
		{"failed", "running", true},      // terminal
		{"cancelled", "running", true},   // terminal
		{"interrupted", "running", true}, // terminal
		{"queued", "completed", true},    // skip running
	}
	for _, tt := range tests {
		err := ValidateRunTransition(tt.from, tt.to)
		if (err != nil) != tt.wantErr {
			t.Errorf("run %q→%q: got err=%v, wantErr=%v", tt.from, tt.to, err, tt.wantErr)
		}
	}
}
