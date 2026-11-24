//go:build !with_gvisor

package wireguard

// CoalescingWriter is the with_gvisor receive coalescing; without the
// userspace stack there is nothing to coalesce for.
type CoalescingWriter struct{}

func NewCoalescingWriter(device Device) *CoalescingWriter {
	return nil
}

func (w *CoalescingWriter) Write(packets [][]byte) {}
