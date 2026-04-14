package channel

// SenderIdentity represents a platform sender with an ordered set of
// candidate external IDs. ID is the preferred canonical identifier; IDs keeps
// the full ordered fallback set, most stable first.
type SenderIdentity struct {
	ID  string
	IDs []string
}

// NewSenderIdentity builds a deduplicated sender identity from one or more
// candidate IDs. The first non-empty ID becomes the preferred ID.
func NewSenderIdentity(ids ...string) SenderIdentity {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	identity := SenderIdentity{IDs: out}
	if len(out) > 0 {
		identity.ID = out[0]
	}
	return identity
}
