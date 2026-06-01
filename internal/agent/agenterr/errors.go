package agenterr

import "errors"

// ErrChatTimeout is emitted when an agent chat turn exceeds its deadline.
var ErrChatTimeout = errors.New("chat timeout exceeded")

// ErrSessionBusy is returned when a session already has an active chat turn.
var ErrSessionBusy = errors.New("session has an active turn")
