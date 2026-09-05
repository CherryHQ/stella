package channel

import "errors"

// ErrAgentAccessDenied is the public category for a channel-bound agent
// rejecting an incoming operation. Its wording is part of transport output.
var ErrAgentAccessDenied = errors.New("you don't have access to this agent, contact an admin")

// ErrAgentAccessForbidden is the public category for a general agent access
// denial. It remains distinct from ErrAgentAccessDenied because Discord's
// guest attachment path historically recognized only this category.
var ErrAgentAccessForbidden = errors.New("agent access forbidden")
