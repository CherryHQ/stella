package runtime

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// titleMaxRunes bounds a generated session title. It counts runes, not bytes:
// slicing a string at a byte offset cuts a multi-byte character in half and
// stores invalid UTF-8, which every Chinese or emoji message hits.
const titleMaxRunes = 60

// Keys worth showing, most specific first. A machine payload usually carries one
// key naming what happened and another describing it; anything else is metadata
// a person scanning a list does not need.
var (
	titleLabelKeys = []string{"event", "event_type", "type", "action", "name", "subject", "title"}
	titleBodyKeys  = []string{"message", "text", "body", "content", "summary", "description", "prompt"}
)

// autoTitle names a session after its first message.
//
// A webhook posts JSON, so without this the title is a raw payload fragment.
// Note that this parses, while the web client's `sessionDisplayTitle` cannot:
// this runs on the whole message and truncates afterwards, whereas the client
// only ever sees titles that were already cut to 60 characters — mid-value, so
// they never parse. The two exist for that reason and are not redundant. This
// one fixes what gets stored from here on; the client one fixes what is already
// stored.
func autoTitle(msgText string) string {
	if title := jsonTitle(msgText); title != "" {
		return truncateRunes(title, titleMaxRunes)
	}
	return truncateRunes(collapseSpace(msgText), titleMaxRunes)
}

// jsonTitle renders a JSON payload as "label · body", or "" when msgText is not
// JSON or carries no readable string value.
func jsonTitle(msgText string) string {
	trimmed := strings.TrimSpace(msgText)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return ""
	}
	pairs := jsonStringPairs(trimmed)
	if len(pairs) == 0 {
		return ""
	}

	label := pickKey(pairs, titleLabelKeys)
	body := pickKey(pairs, titleBodyKeys)
	switch {
	case label != "" && body != "":
		return label + " · " + body
	case label != "":
		return label
	case body != "":
		return body
	}

	// No key we recognize. Two values in document order still beat raw JSON,
	// because they are what a human would read off the payload anyway.
	var parts []string
	for _, p := range pairs {
		if parts = append(parts, p.value); len(parts) == 2 {
			break
		}
	}
	return strings.Join(parts, " · ")
}

type jsonPair struct {
	key, value string
	depth      int // nesting level of the object holding the key; 0 is top level
}

// jsonFrame is one open container. `hasKey` is a separate flag rather than a
// `key != ""` test because the empty string is a legal JSON key: using the value
// as its own presence sentinel makes `{"":"noise","event":"deploy"}` read
// "noise" as a key and pair up (event, deploy)'s neighbours instead.
type jsonFrame struct {
	key     string
	hasKey  bool
	inArray bool
}

// jsonStringPairs walks the payload in document order and returns every
// key/string-value pair, at any depth. Order matters: it is the fallback's only
// tie-breaker, and Go maps would randomize it between identical payloads.
func jsonStringPairs(payload string) []jsonPair {
	dec := json.NewDecoder(strings.NewReader(payload))
	var (
		pairs  []jsonPair
		frames []jsonFrame
	)
	// clearKey marks the innermost still-open container as expecting a key
	// again, which is what "a value just ended" means.
	clearKey := func() {
		if n := len(frames); n > 0 {
			frames[n-1].hasKey = false
		}
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			// Truncated or malformed input still yields whatever it read first.
			return pairs
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				frames = append(frames, jsonFrame{})
			case '[':
				frames = append(frames, jsonFrame{inArray: true})
			case '}', ']':
				if len(frames) > 0 {
					frames = frames[:len(frames)-1]
				}
				// The container that just closed *was* the parent's pending
				// value. Leaving the key set would make the next sibling key
				// read as its value, so `{"meta":{...},"event":"push"}` would
				// yield the pair (meta, event).
				clearKey()
			}
		case string:
			depth := len(frames) - 1
			if depth < 0 {
				return pairs
			}
			frame := &frames[depth]
			// Inside an object, a string alternates key then value.
			if !frame.inArray && !frame.hasKey {
				frame.key, frame.hasKey = t, true
				continue
			}
			if v := collapseSpace(t); v != "" {
				pairs = append(pairs, jsonPair{key: frame.key, value: v, depth: depth})
			}
			frame.hasKey = false
		default:
			// Numbers, bools and null are values; clear any pending key so the
			// next string is read as a key again.
			clearKey()
		}
	}
}

// pickKey chooses the value whose key best names the payload.
//
// Shallowest wins before best-named does. A webhook says what happened at the
// top level and carries metadata underneath, so ranking by key alone lets a
// nested `type` outrank a top-level `action` — `{"action":"opened","sender":
// {"type":"User"}}` gets titled after the sender. Depth is the stronger signal:
// the outer object is the message, the inner ones describe its parts.
func pickKey(pairs []jsonPair, wanted []string) string {
	deepest := 0
	for _, p := range pairs {
		deepest = max(deepest, p.depth)
	}
	for depth := 0; depth <= deepest; depth++ {
		for _, want := range wanted {
			for _, p := range pairs {
				if p.depth == depth && strings.EqualFold(p.key, want) {
					return p.value
				}
			}
		}
	}
	return ""
}

// collapseSpace flattens newlines and runs of whitespace so a multi-line message
// renders as one line in a list.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncateRunes cuts to at most max runes, preferring a word boundary when one
// sits far enough in to leave a useful title.
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	cut, n := len(s), 0
	for i := range s {
		if n == max {
			cut = i
			break
		}
		n++
	}
	head := s[:cut]
	if idx := strings.LastIndex(head, " "); idx > 20 {
		head = head[:idx]
	}
	return head + "…"
}
