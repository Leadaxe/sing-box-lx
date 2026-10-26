// Package lxtrace is the stage trace of an active connection test
// (SPEC 139, StartConnectionTest). The test handler puts a Trace into the
// context it drives the pipeline with; one-line `// lx:` insertions at
// pipeline points (route decision, dial, detour, lookup, DNS, TLS, ...) open
// and close stages against it. Nesting follows ordinary context passing:
// a stage begun in a child context is a descendant of the stage in its
// parent context, so the tree of the trace is the real call tree of the
// pipeline, with no knowledge of levels at the insertion points.
//
// Without a Trace in the context every function is a single ctx.Value
// lookup and a return — the production path pays nothing else (SPEC 139
// §5.12). In a build without the with_lx_command tag the whole package is a
// no-op stub, so the insertions compile in any build.
package lxtrace
