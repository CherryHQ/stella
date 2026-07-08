package server

import "github.com/CherryHQ/stella/internal/authz"

func toolIdentity(info *AuthInfo) authz.Identity {
	if info == nil {
		return authz.Identity{}
	}
	return authz.Identity{UserID: info.UserID}
}
