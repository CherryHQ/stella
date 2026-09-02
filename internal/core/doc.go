// Package core groups the leaf kernels every other internal package needs and
// that need almost nothing back: tool metadata, agent context keys, sentinel
// errors, agent access checks, provider credentials.
//
// Admission rule, enforced by boundary_test.go in this directory:
// internal/core/** may import the standard library, third-party modules,
// github.com/CherryHQ/stella/pkg/**, other internal/core/**,
// internal/authz, and internal/platform/config — nothing else inside the repo.
//
// A package that needs more than that is not a kernel; it is a domain package
// with a runtime dependency and it stays where it lives. internal/agent/settingspolicy
// is the worked example: its Available() takes a runtime.RunnerParams.
//
// This directory holds no code of its own; each kernel is a subpackage keeping
// its original package name (toolmeta, access, agentctx, agenterr, providercred),
// so moving a package here changes import lines and nothing else.
package core
