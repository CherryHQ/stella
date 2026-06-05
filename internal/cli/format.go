package cli

// Truncate shortens s to max runes, appending "…" when trimmed.
func Truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func DerefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func DerefInt(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}

// ShortID returns the first 8 chars of an ID for display.
func ShortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
