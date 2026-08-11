//go:build !unix

package home

import "errors"

var errUnsupported = errors.New("home: persistent workspaces require POSIX openat support")

func openWorkspaceRoot(string) (int, error) { return -1, errUnsupported }
func (m *WorkspaceManager) ensureChain(...string) error { return errUnsupported }
func (m *WorkspaceManager) agentIDOccupied(string) (bool, error) { return true, errUnsupported }
func closeWorkspaceRoot(int) error { return nil }
