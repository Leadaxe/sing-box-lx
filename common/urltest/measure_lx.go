package urltest

// lx:begin SPEC 138 the context of a delay measurement
//
// URLTest marks the context of its connection: a round_robin group that the
// measurement goes through (an outer group's check, the clash API) leaves it out
// of its self-heal — no watch, no retry, no check — so the measurement shows the
// node as it is.

import "context"

type measureKeyLx struct{}

// WithMeasureLx marks ctx as the context of a delay measurement.
func WithMeasureLx(ctx context.Context) context.Context {
	return context.WithValue(ctx, measureKeyLx{}, true)
}

// IsMeasureLx reports whether ctx is the context of a delay measurement.
func IsMeasureLx(ctx context.Context) bool {
	measure, _ := ctx.Value(measureKeyLx{}).(bool)
	return measure
}

// lx:end SPEC 138
