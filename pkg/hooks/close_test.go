package hooks

import (
	"errors"
	"testing"
)

type closeTestHook struct {
	name     string
	closed   bool
	closeErr error
}

type closeTestNonCloser struct{ name string }

func (h closeTestNonCloser) Name() string  { return h.name }
func (h closeTestNonCloser) Priority() int { return 0 }

func (h *closeTestHook) Name() string  { return h.name }
func (h *closeTestHook) Priority() int { return 0 }
func (h *closeTestHook) Close() error {
	h.closed = true
	return h.closeErr
}

func TestClosePlugins(t *testing.T) {
	closable := &closeTestHook{name: "closable"}
	failing := &closeTestHook{name: "failing", closeErr: errors.New("close failed")}
	ClosePlugins([]HookPlugin{closable, failing})
	if !closable.closed || !failing.closed {
		t.Fatal("ClosePlugins did not close every closable hook")
	}
}

func TestClosePluginsIgnoresNonClosers(t *testing.T) {
	ClosePlugins([]HookPlugin{closeTestNonCloser{name: "noop"}})
}
