package tracehook

import (
	"fmt"

	"github.com/CherryHQ/stella/internal/platform/observability"
)

func logErrorClass(err error) string {
	if err == nil {
		return "none"
	}
	return fmt.Sprintf("%T", err)
}

func logRawError(component, class string, err error) {
	if err == nil {
		return
	}
	observability.ConsoleOnlyLogger().Warn(component, "error.type", logErrorClass(err), "error.class", class, "error", err)
}
