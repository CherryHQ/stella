package admin

import (
	"strings"
	"testing"
)

func TestValidateSchedule_Cron(t *testing.T) {
	err := validateSchedule(schedulerJobJSON{Cron: "0 * * * *"})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateSchedule_Every(t *testing.T) {
	err := validateSchedule(schedulerJobJSON{Every: "1h"})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateSchedule_At(t *testing.T) {
	err := validateSchedule(schedulerJobJSON{At: "09:00"})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateSchedule_None(t *testing.T) {
	err := validateSchedule(schedulerJobJSON{})
	if err == nil {
		t.Error("expected error when no schedule type is set")
	}
}

func TestValidateSchedule_Multiple(t *testing.T) {
	err := validateSchedule(schedulerJobJSON{Cron: "* * * * *", Every: "1h"})
	if err == nil {
		t.Error("expected error when multiple schedule types are set")
	}
}

func TestValidateSchedule_InvalidDuration(t *testing.T) {
	err := validateSchedule(schedulerJobJSON{Every: "not-a-duration"})
	if err == nil {
		t.Error("expected error for invalid duration")
	}
}

func TestGenerateShortID(t *testing.T) {
	id1 := generateShortID()
	id2 := generateShortID()

	// 4 bytes = 8 hex chars.
	if len(id1) != 8 {
		t.Errorf("expected 8 char ID, got %d: %q", len(id1), id1)
	}
	if id1 == id2 {
		t.Error("expected unique IDs")
	}
	// Should only contain hex characters.
	for _, c := range id1 {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Errorf("unexpected character %q in ID %q", c, id1)
		}
	}
}
