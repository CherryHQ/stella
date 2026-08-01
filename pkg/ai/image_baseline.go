package ai

import (
	"errors"
	"strings"
)

// ValidateImageBaselineText validates the stable OCR-and-scene storage shape.
// OCR text may contain headings; only the exact blank-line Scene delimiter is
// structural. Once Scene starts, another markdown section would make the
// canonical shape ambiguous and is rejected.
func ValidateImageBaselineText(text string) error {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "## Text\n") {
		return errors.New("baseline must start with ## Text")
	}
	parts := strings.Split(text, "\n\n## Scene\n")
	if len(parts) != 2 || strings.TrimSpace(strings.TrimPrefix(parts[0], "## Text\n")) == "" || strings.TrimSpace(parts[1]) == "" {
		return errors.New("baseline must contain nonempty ## Text and ## Scene sections exactly once")
	}
	if strings.Contains(parts[1], "\n\n## ") {
		return errors.New("baseline scene must not contain another markdown section")
	}
	return nil
}
