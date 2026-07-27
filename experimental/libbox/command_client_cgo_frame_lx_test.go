//go:build with_lx_command

package libbox

import (
	"reflect"
	"testing"
)

// TestCommandClientNoPointerBearingCValueReturns guards the crash fixed in SPEC 037: an
// exported CommandClient method returning a bare string (or []byte) binds to a cgo frame
// whose result slot is an nstring/nbyteslice — a C struct carrying a pointer. cgo marks
// that frame __attribute__((__packed__)), so the C local loses its 8-byte alignment
// requirement and lands 4-byte aligned on arm64, while the Go side writes the
// pointer-bearing result slot through a write barrier that requires 8. The mismatch is a
// hard kill in runtime.bulkBarrierPreWrite ("unaligned arguments"), not a recoverable
// panic — it took down the tunnel on every GetRunningConfig call in v1.14.0-lx.16.
//
// Returning an object instead keeps the frame pointer-free (gomobile passes a refnum, an
// int32), which is the shape every other method here already uses. Scalars are fine for
// the same reason: no pointer in the frame means no barrier.
//
// This is deliberately a whole-surface check rather than a test of one method: the defect
// is a property of the return type, so any future method reintroducing that shape is the
// same bug again.
func TestCommandClientNoPointerBearingCValueReturns(t *testing.T) {
	t.Parallel()
	clientType := reflect.TypeOf(&CommandClient{})
	for i := 0; i < clientType.NumMethod(); i++ {
		method := clientType.Method(i)
		signature := method.Type
		for j := 0; j < signature.NumOut(); j++ {
			out := signature.Out(j)
			switch {
			case out.Kind() == reflect.String:
				t.Errorf("%s returns a bare string: gomobile binds this to a packed cgo frame holding an nstring, which kills the process in bulkBarrierPreWrite on arm64 — return an object with a Content()-style getter instead", method.Name)
			case out.Kind() == reflect.Slice && out.Elem().Kind() == reflect.Uint8:
				t.Errorf("%s returns []byte: gomobile binds this to a packed cgo frame holding an nbyteslice, which has the same pointer-in-frame alignment kill as a bare string — return an object instead", method.Name)
			}
		}
	}
}

// TestRunningConfigContent pins the accessor the Android client reads the SPEC 037
// snapshot through, so the wrapper cannot quietly lose its getter.
func TestRunningConfigContent(t *testing.T) {
	t.Parallel()
	const document = `{"log":{"level":"debug"}}`
	config := &RunningConfig{content: document}
	if config.Content() != document {
		t.Fatalf("Content() = %q, want %q", config.Content(), document)
	}
}
