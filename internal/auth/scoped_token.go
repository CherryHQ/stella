package auth

import pkgauth "github.com/CherryHQ/stella/pkg/auth"

// Re-export scoped-token primitives from pkg/auth so existing internal
// callers keep compiling without import changes.

type ScopedTokenClaims = pkgauth.ScopedTokenClaims

const ScopedTokenPrefix = pkgauth.ScopedTokenPrefix

var DefaultSandboxScopes = pkgauth.DefaultSandboxScopes

var (
	SignScopedToken            = pkgauth.SignScopedToken
	VerifyScopedToken          = pkgauth.VerifyScopedToken
	ParseScopedTokenUnverified = pkgauth.ParseScopedTokenUnverified
	IsScopedToken              = pkgauth.IsScopedToken
)
