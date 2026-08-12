//go:build !unix

package home

import (
	"errors"
	"os"
)

var errUnsupported = errors.New("home: persistent workspaces require POSIX openat support")

func openWorkspaceRoot(string) (int, error)                      { return -1, errUnsupported }
func (m *WorkspaceManager) ensureChain(...string) error          { return errUnsupported }
func (m *WorkspaceManager) syncChain(...string) error            { return errUnsupported }
func (m *WorkspaceManager) agentIDOccupied(string) (bool, error) { return true, errUnsupported }
func (m *WorkspaceManager) openOperationsRoot(...string) (*os.Root, error) {
	return nil, errUnsupported
}
func openRootFile(*os.Root, string, int, os.FileMode) (*os.File, error) {
	return nil, errUnsupported
}
func closeWorkspaceRoot(int) error { return nil }
