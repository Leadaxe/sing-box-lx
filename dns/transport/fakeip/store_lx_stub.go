//go:build !with_lx_command

package fakeip

import "net/netip"

// Build-tag twin of store_lx.go (SPEC 139): the Create insertion compiles and
// records nothing without with_lx_command.

func lxNoteCreate(s *Store, previous4, previous6 netip.Addr) {}
