package option

// RecordFragmentEnabled reports whether `record_fragment` is set to true.
//
// The field is a pointer so that an explicit `false` stays distinguishable from
// an absent key: under a `detour` the absent key turns record fragmentation on
// by default, an explicit `false` keeps it off. Everything that only needs the
// effective on/off value reads it through here. lx: SPEC 156.
func (o OutboundTLSOptions) RecordFragmentEnabled() bool {
	return o.RecordFragment != nil && *o.RecordFragment
}
