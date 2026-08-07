//go:build !with_lx_command

// Stub keeps the package buildable without the with_lx_command tag: the lxd
// daemon is an lx-only surface, and `go build ./...` must not break on a
// package whose files are all tag-gated.
package lxd
