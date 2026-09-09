package server

import (
	apiserver "github.com/CherryHQ/stella/api/server"
)

func stringPtrOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

var _ apiserver.ServerInterface = (*Server)(nil)
