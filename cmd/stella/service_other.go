//go:build !darwin && !linux

package main

import "errors"

var errServiceNotSupported = errors.New("stella service is not supported on this platform")

type stubManager struct{}

func newServiceManager() serviceManager             { return &stubManager{} }
func (s *stubManager) Install(_ bool) error         { return errServiceNotSupported }
func (s *stubManager) Uninstall(_ bool) error       { return errServiceNotSupported }
func (s *stubManager) Start() error                 { return errServiceNotSupported }
func (s *stubManager) Stop() error                  { return errServiceNotSupported }
func (s *stubManager) Restart() error               { return errServiceNotSupported }
func (s *stubManager) Status() error                { return errServiceNotSupported }
func (s *stubManager) Logs(_ bool) error            { return errServiceNotSupported }
