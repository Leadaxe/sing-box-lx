package constant

// lx: SPEC 073 — outbound `chain`: виртуальная цепочка хопов из групп и узлов.
// Константа живёт в lx-файле, чтобы upstream-список типов (proxy.go) не трогать.
const TypeChain = "chain"

// TypeChainHop is the internal hop of a chain (lx: SPEC 165): the rewrite of
// the config inserts one per position, tagged "<chain>#<i>". A user config
// that names the type fails the load.
const TypeChainHop = "chain-hop"
