package impair

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// reportCPU adds the process CPU per op and the share of one core that
// 10 Mbit/s of this traffic costs (where the process CPU time is known,
// see cpuTime).
func reportCPU(b *testing.B, start time.Duration, bytesPerOp int) {
	end, ok := cpuTime()
	if !ok {
		return
	}
	perOp := float64(end-start) / float64(b.N)
	b.ReportMetric(perOp, "cpu-ns/op")
	opsPer10Mbit := 10e6 / 8 / float64(bytesPerOp)
	b.ReportMetric(perOp*opsPer10Mbit/1e9*100, "core-%/10Mbit")
}

// The cost of the impairment itself, on discarding carriers: what impair
// adds on top of the carrier's own syscalls. CPU per Mbit/s = ns/op ×
// (1e6 / 8 / bytes per op) ns of CPU per second.

type discardPacket struct {
	written atomic.Int64
	closed  chan struct{}
}

func (d *discardPacket) ReadFrom([]byte) (int, net.Addr, error) {
	<-d.closed
	return 0, nil, net.ErrClosed
}

func (d *discardPacket) WriteTo(p []byte, _ net.Addr) (int, error) {
	d.written.Add(1)
	return len(p), nil
}
func (d *discardPacket) Close() error                     { close(d.closed); return nil }
func (d *discardPacket) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (d *discardPacket) SetDeadline(time.Time) error      { return nil }
func (d *discardPacket) SetReadDeadline(time.Time) error  { return nil }
func (d *discardPacket) SetWriteDeadline(time.Time) error { return nil }

type discardStream struct {
	discardPacket
}

func (d *discardStream) Read([]byte) (int, error) {
	<-d.closed
	return 0, net.ErrClosed
}

func (d *discardStream) Write(p []byte) (int, error) {
	d.written.Add(int64(len(p)))
	return len(p), nil
}
func (d *discardStream) RemoteAddr() net.Addr { return &net.TCPAddr{} }

func benchShaper(b *testing.B, link LinkOptions) *shaper {
	cfg, err := resolve(Options{LinkParams: &LinkParams{ImpairLinkOptions: link}, Seed: seed(1)}, nil)
	if err != nil {
		b.Fatal(err)
	}
	s := newShaper(cfg, realClock())
	b.Cleanup(s.close)
	return s
}

// BenchmarkPacketUp: a 1400-byte datagram through the up bottleneck, the
// heap and the pump to the carrier, in bursts of 64 that the pump drains
// before the next (the steady state: buffers come back to the pool).
func BenchmarkPacketUp(b *testing.B) {
	s := benchShaper(b, LinkOptions{Rate: "10gbit", Queue: "10s", Jitter: "100us", Loss: "0.1%"})
	under := &discardPacket{closed: make(chan struct{})}
	pc := newPacketConn(s, under, nil)
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}
	payload := make([]byte, 1400)
	b.SetBytes(1400)
	b.ReportAllocs()
	b.ResetTimer()
	start, _ := cpuTime()
	for i := range b.N {
		pc.WriteTo(payload, addr)
		if i%64 == 63 {
			for s.pumps[dirUp].len() > 0 {
				time.Sleep(50 * time.Microsecond)
			}
		}
	}
	for s.pumps[dirUp].len() > 0 {
		time.Sleep(50 * time.Microsecond)
	}
	b.StopTimer()
	reportCPU(b, start, 1400)
	pc.Close()
}

// BenchmarkStreamUp: 64 KB writes cut into segments, paced, through the
// delay line to the carrier.
func BenchmarkStreamUp(b *testing.B) {
	s := benchShaper(b, LinkOptions{Rate: "10gbit", Queue: "10s", Delay: "1ms"})
	under := &discardStream{discardPacket{closed: make(chan struct{})}}
	c := newStreamConn(s, under, nil)
	payload := make([]byte, 64<<10)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	start, _ := cpuTime()
	for range b.N {
		if _, err := c.Write(payload); err != nil {
			b.Fatal(err)
		}
	}
	for under.written.Load() < int64(b.N*len(payload)) {
		time.Sleep(time.Millisecond)
	}
	b.StopTimer()
	reportCPU(b, start, len(payload))
	c.Close()
}

// BenchmarkAdmitPacket is the model alone.
func BenchmarkAdmitPacket(b *testing.B) {
	s := benchShaper(b, LinkOptions{
		Rate: "10gbit", Queue: "10s", Delay: "1ms", Jitter: "100us", Loss: "0.1%", LossBurst: floatp(3),
		Vary: &VaryOptions{Min: "5gbit", Max: "10gbit", Every: "10ms"},
	})
	b.ReportAllocs()
	for range b.N {
		s.model.admitPacket(nil, dirUp, 1400)
	}
}
