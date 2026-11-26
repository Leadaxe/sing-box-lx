package wireguard

// CoalescingWriter was the gVisor GRO receive coalescing of the 1.14 line
// (SPEC 021). The 1.15 stack has no GRO entry point, so the MASQUE pump
// writes batches to the device directly.
type CoalescingWriter struct{}

func NewCoalescingWriter(device Device) *CoalescingWriter {
	return nil
}

func (w *CoalescingWriter) Write(packets [][]byte) {}
