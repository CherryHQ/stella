package channel

import "errors"

// `/new` rotates a chat onto a fresh session. Only direct messages (and linked
// private channels) can be reset this way: a group's context is shared by every
// member, so no single member's chat command may clear it, and a group `/new`
// is answered with an explicit refusal instead.
//
// Skipping nothing here would be simpler, but `/new` is destructive: it must
// bring its own dedup, because a platform redelivery that rotated a second time
// would silently archive whatever the chat said in between.

// newSessionCommand is the command string recorded on a `/new` receipt.
const newSessionCommand = "/new"

// errUnidentifiedCommand reports a destructive command on a delivery that
// carries no stable message id. Without an identity a redelivery cannot be
// told apart from a new command, so the command fails closed instead of
// running unguarded.
var errUnidentifiedCommand = errors.New("command message has no stable identity")
