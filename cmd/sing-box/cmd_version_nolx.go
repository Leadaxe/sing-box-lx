//go:build !with_lxd

package main

// versionExtraLx: no lxd in this build, nothing to add.
func versionExtraLx() string { return "" }
