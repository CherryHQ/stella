package authz

// InvocationFacts carries validated, typed policy facts for one authorization
// request — the request-scoped context a decision may depend on that is not part
// of the durable Authority. It is deliberately a closed typed struct, not a
// map[string]any: transport and plugin code cannot smuggle arbitrary attributes
// across the policy boundary. Fields are unexported and set through the builder
// so an invalid fact set cannot be constructed inline.
//
// Two facts exist today:
//   - a dedicated channel binding, the invocation-scoped grant to run a
//     channel's configured agent;
//   - the triggering group member, retained as audit attribution for a group
//     turn. It is a fact, never identity: it cannot grant that member's
//     private-user capabilities to the group.
type InvocationFacts struct {
	channelBinding ChannelBinding
	groupSpeaker   UserID
}

// ChannelBinding identifies a dedicated channel binding presented as an
// invocation grant. The zero value is "no binding".
type ChannelBinding struct {
	channelID string
	agentID   AgentID
}

// NewChannelBinding constructs a binding. Both fields are required; the zero
// ChannelBinding means "unset" and is returned for an empty pair.
func NewChannelBinding(channelID string, agentID AgentID) (ChannelBinding, bool) {
	if channelID == "" || agentID == "" {
		return ChannelBinding{}, false
	}
	return ChannelBinding{channelID: channelID, agentID: agentID}, true
}

// ChannelID returns the bound channel id.
func (b ChannelBinding) ChannelID() string { return b.channelID }

// AgentID returns the bound agent id.
func (b ChannelBinding) AgentID() AgentID { return b.agentID }

// Set reports whether the binding is populated.
func (b ChannelBinding) Set() bool { return b.channelID != "" && b.agentID != "" }

// FactsBuilder assembles an InvocationFacts value. It is the only way to set
// facts, keeping the InvocationFacts fields unexported and the value immutable
// once built.
type FactsBuilder struct {
	facts InvocationFacts
}

// NewFactsBuilder returns an empty builder. Empty facts are valid: most requests
// carry none.
func NewFactsBuilder() *FactsBuilder { return &FactsBuilder{} }

// WithChannelBinding attaches a dedicated channel binding.
func (b *FactsBuilder) WithChannelBinding(binding ChannelBinding) *FactsBuilder {
	b.facts.channelBinding = binding
	return b
}

// WithGroupSpeaker attaches the triggering group member as audit attribution.
func (b *FactsBuilder) WithGroupSpeaker(member UserID) *FactsBuilder {
	b.facts.groupSpeaker = member
	return b
}

// Build returns the immutable facts value.
func (b *FactsBuilder) Build() InvocationFacts { return b.facts }

// ChannelBinding returns the dedicated channel binding fact, if any.
func (f InvocationFacts) ChannelBinding() ChannelBinding { return f.channelBinding }

// GroupSpeaker returns the triggering group member (audit attribution only).
func (f InvocationFacts) GroupSpeaker() UserID { return f.groupSpeaker }
