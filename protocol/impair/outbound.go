// Package impair is a TEST outbound: it dials like `direct` (or through its
// `detour`) and degrades every connection made through it — a shared
// bottleneck rate with a queue, added delay and jitter, random or bursty
// loss, rate swings, stalls and scheduled outages — in both directions,
// from this end alone, without privileges. A test tool, not for production
// traffic; see docs-lx/lx-impair.md.
package impair

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/dialer"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

// RegisterOutbound registers the `impair` outbound type.
func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[Options](registry, C.TypeImpair, New)
}

var _ adapter.Outbound = (*Outbound)(nil)

// Outbound is the impair outbound.
type Outbound struct {
	outbound.Adapter
	logger logger.ContextLogger
	dialer N.Dialer
	detour string
	cfg    *config
	shaper *shaper

	closeOnce sync.Once
}

// New is the registry constructor.
func New(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options Options) (adapter.Outbound, error) {
	var userProfiles map[string]ProfileOption
	if lx := service.PtrFromContext[option.LXResolved](ctx); lx != nil {
		userProfiles = lx.ImpairProfiles
	}
	cfg, err := resolve(options, userProfiles)
	if err != nil {
		return nil, E.Cause(err, "impair")
	}
	if options.Detour == "" {
		options.UDPFragmentDefault = true // as direct
	}
	carrier, err := dialer.New(ctx, options.DialerOptions, true)
	if err != nil {
		return nil, err
	}
	shaper := newShaper(cfg, realClock())
	if options.ConnectTimeout > 0 {
		shaper.connectTimeout = time.Duration(options.ConnectTimeout)
	}
	shaper.logf = func(debug bool, text string) {
		if debug {
			logger.Debug("impair outbound ", tag, ": ", text)
		} else {
			logger.Info("impair outbound ", tag, ": ", text)
		}
	}
	return &Outbound{
		Adapter: outbound.NewAdapterWithDialerOptions(C.TypeImpair, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.DialerOptions),
		logger:  logger,
		dialer:  carrier,
		detour:  options.Detour,
		cfg:     cfg,
		shaper:  shaper,
	}, nil
}

// Start: Close is registered in the scope at Initialize (the upstream
// lifecycle calls nothing else on shutdown), so a core restarted inside one
// process (Android, the launcher, SIGHUP) stops the event loop, the pumps
// and the connections of the old instance.
func (h *Outbound) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage == adapter.StartStateInitialize {
		scope.Add(h.Close)
		return nil
	}
	if stage != adapter.StartStateStart {
		return nil
	}
	through := h.detour
	if through == "" {
		through = "the direct dialer"
	}
	h.logger.Warn("impair outbound ", h.Tag(), ": traffic through ", through, " is degraded on purpose (test tool): ", h.describe())
	h.shaper.startEvents()
	return nil
}

func (h *Outbound) describe() string {
	text := newLine(h.cfg.line, 0, 0, 0).describe()
	if n := len(h.cfg.sublinks); n > 0 {
		text += "; " + strconv.Itoa(n) + " sublinks"
	}
	if n := len(h.cfg.schedule); n > 0 {
		text += "; schedule of " + strconv.Itoa(n) + " entries"
		if h.cfg.repeat > 0 {
			text += " repeating every " + h.cfg.repeat.String()
		}
	}
	return text
}

// connInfo is what the sublinks see of a new connection: the destination,
// with the domain from the routing metadata when the destination is
// already an address.
func connInfoOf(ctx context.Context, network string, destination M.Socksaddr) connInfo {
	info := connInfo{network: network, domain: destination.Fqdn, port: destination.Port}
	if destination.IsIP() {
		info.addr = destination.Addr
	}
	if info.domain == "" {
		if metadata := adapter.ContextFrom(ctx); metadata != nil {
			info.domain = metadata.Domain
			if !info.addr.IsValid() && metadata.Destination.IsIP() {
				info.addr = metadata.Destination.Addr
			}
		}
	}
	return info
}

// describeLink is a direction's link for the log: rate, delay, loss.
func describeLink(p *params) string {
	var text string
	if p.vary != nil {
		text = "rate " + formatRate(p.vary.min) + ".." + formatRate(p.vary.max) + "/" + p.vary.every.String()
	} else if p.rate > 0 {
		text = "rate " + formatRate(p.rate)
	} else {
		text = "rate unlimited"
	}
	text += " delay " + p.delay.String()
	if p.jitter > 0 {
		text += "±" + p.jitter.String()
	}
	if p.loss > 0 {
		text += " loss " + strconv.FormatFloat(p.loss*100, 'g', 4, 64) + "%"
	}
	return text
}

func formatRate(bits float64) string {
	switch {
	case bits >= 1e9:
		return strconv.FormatFloat(bits/1e9, 'g', 4, 64) + "gbit"
	case bits >= 1e6:
		return strconv.FormatFloat(bits/1e6, 'g', 4, 64) + "mbit"
	case bits >= 1e3:
		return strconv.FormatFloat(bits/1e3, 'g', 4, 64) + "kbit"
	}
	return strconv.FormatFloat(bits, 'g', 4, 64) + "bit"
}

func (h *Outbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	network = N.NetworkName(network)
	if network != N.NetworkTCP && network != N.NetworkUDP {
		return nil, E.New("impair: unsupported network ", network)
	}
	sub := h.shaper.model.pick(connInfoOf(ctx, network, destination))
	if err := h.shaper.beforeDial(ctx, sub); err != nil {
		return nil, err
	}
	conn, err := h.dialer.DialContext(ctx, network, destination)
	if err != nil {
		return nil, err
	}
	return h.shaper.wrapConn(network, conn, sub), nil
}

func (h *Outbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	sub := h.shaper.model.pick(connInfoOf(ctx, N.NetworkUDP, destination))
	if err := h.shaper.beforeDial(ctx, sub); err != nil {
		return nil, err
	}
	conn, err := h.dialer.ListenPacket(ctx, destination)
	if err != nil {
		return nil, err
	}
	return newPacketConn(h.shaper, conn, sub), nil
}

func (h *Outbound) Close() error {
	h.closeOnce.Do(func() {
		for dir := range dirNames {
			stats := h.shaper.model.stats(dir)
			h.logger.Info("impair outbound ", h.Tag(), " ", dirNames[dir], ": ", stats.Units, " units ", stats.Bytes, " bytes; dropped queue ", stats.DropQueue, " loss ", stats.DropLoss, " outage ", stats.DropOutage, "; stream stalls ", stats.Stalls)
		}
		h.shaper.close()
	})
	return nil
}
