// Package manifest owns manifest-declared plugins: loading the builtin
// manifest, applying operator overrides, validating a plugin's shape, and
// reconciling its mise-installed runtime against what the manifest asks for.
//
// Manifest plugins describe traits; they can never request a host capability.
// That line is enforced in internal/plugin/host.
package manifest
