package plugin

import "io/fs"

// ResourceFile preserves binary bytes and executable bits when a complete
// package is copied or only an unrelated file is edited.
type ResourceFile struct {
	Data []byte
	Mode fs.FileMode
}
