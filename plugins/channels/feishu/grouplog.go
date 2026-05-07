package feishu

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type GroupEntry struct {
	Timestamp time.Time
	SenderID  string
	Name      string
	Text      string
	MessageID string
}

type GroupLog struct {
	mu      sync.Mutex
	entries []GroupEntry
	cap     int
	head    int
	count   int
}

func NewGroupLog(capacity int) *GroupLog {
	if capacity <= 0 {
		capacity = 100
	}
	return &GroupLog{
		entries: make([]GroupEntry, capacity),
		cap:     capacity,
	}
}

func (g *GroupLog) Append(entry GroupEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.entries[g.head] = entry
	g.head = (g.head + 1) % g.cap
	if g.count < g.cap {
		g.count++
	}
}

func (g *GroupLog) Recent(n int) []GroupEntry {
	g.mu.Lock()
	defer g.mu.Unlock()

	if n <= 0 {
		return nil
	}
	if n > g.count {
		n = g.count
	}

	result := make([]GroupEntry, n)
	// oldest of the n entries starts at (head - n) mod cap
	start := (g.head - n + g.cap) % g.cap
	for i := range n {
		result[i] = g.entries[(start+i)%g.cap]
	}
	return result
}

func (g *GroupLog) FormatContext(n int, nameResolver func(string) string) string {
	entries := g.Recent(n)
	if len(entries) == 0 {
		return ""
	}

	var b strings.Builder
	for _, e := range entries {
		name := ""
		if nameResolver != nil {
			name = nameResolver(e.SenderID)
		}
		if name == "" {
			name = e.Name
		}
		if name == "" {
			name = e.SenderID
		}
		fmt.Fprintf(&b, "[%s %s]: %s\n", name, e.Timestamp.Format("15:04"), e.Text)
	}
	return strings.TrimRight(b.String(), "\n")
}
