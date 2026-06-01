package agenterr

import "errors"

// ErrChatTimeout is emitted when an agent chat turn exceeds its deadline.
var ErrChatTimeout = errors.New("chat timeout exceeded")
