package session

import (
	"strings"
	"unicode/utf8"
)

// MaxSynchronousOutputBytes bounds output retained while a caller drains a
// synchronous child Session stream. Draining continues after this limit so the
// producer can finish and release its busy guard.
const MaxSynchronousOutputBytes = 64_000

// OutputCollector keeps a UTF-8-safe prefix while allowing callers to drain an
// arbitrarily large stream without retaining it all in memory.
type OutputCollector struct {
	text      strings.Builder
	truncated bool
}

// Write retains as much of text as fits and records whether any bytes were
// omitted.
func (c *OutputCollector) Write(text string) {
	if c.truncated {
		return
	}
	remaining := MaxSynchronousOutputBytes - c.text.Len()
	if remaining <= 0 {
		c.truncated = c.truncated || text != ""
		return
	}
	if len(text) <= remaining {
		c.text.WriteString(text)
		return
	}
	end := remaining
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	c.text.WriteString(text[:end])
	c.truncated = true
}

func (c *OutputCollector) String() string { return c.text.String() }

func (c *OutputCollector) Truncated() bool { return c.truncated }
