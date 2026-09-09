package plugin

import (
	"errors"

	"github.com/CherryHQ/stella/internal/authz"
)

var (
	// ErrConflict is shared by the file resource API and the migration writer.
	ErrConflict  = errors.New("plugin: revision conflict")
	ErrNotFound  = authz.ErrNotFound
	ErrForbidden = authz.ErrForbidden
)
