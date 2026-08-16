package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
)

func TestGoroutineDumpRequiresHomeStorageAdmission(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	dumpGoroutines(t.Context(), func(context.Context) error { return errors.New("storage closed") })
	if _, err := os.Stat(filepath.Join(home, "dumps")); !os.IsNotExist(err) {
		t.Fatalf("dump directory exists after closed admission: %v", err)
	}
}
