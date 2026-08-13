//go:build !with_lxd

// Stub keeps the package buildable without the with_lxd tag: the lxd daemon is
// an lx-only surface, and `go build ./...` must not break on a package whose
// files are all tag-gated.
//
// The daemon has its OWN tag, separate from with_lx_command (which gates the
// libbox command-protocol extensions of SPEC 014 — URLTestOutbound, GetRules,
// GetGroups). The two were one tag until a build was wanted that keeps those
// RPCs — LxBox needs them — while leaving the daemon out entirely.
package lxd
