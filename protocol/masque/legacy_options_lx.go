// lx: SPEC 062 — masque predates the shared config conventions. Its transport
// lived in `network` (which selects tcp/udp in every other outbound) and its
// TLS settings were flat fields instead of the standard `tls` block.
//
// Both shapes keep working until v1.14.0-lx.30: this file folds the legacy ones
// into the standard shapes before anything else looks at the options, so the
// rest of the outbound only ever sees `Transport` and `TLS`.
package masque

import (
	"context"

	"github.com/sagernet/sing-box/experimental/deprecated"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json/badoption"
)

// resolveLegacyOptions rewrites deprecated fields onto their replacements.
//
// The rule for every pair: an explicit new value wins; a legacy value alone is
// honoured and reported; the two disagreeing is a hard error, because silently
// picking one would leave the user unaware that the other was ignored.
//
// Reporting happens once per outbound, not once per field — a node carrying
// four legacy fields should produce one line, not four.
func resolveLegacyOptions(ctx context.Context, options *option.MASQUEOutboundOptions) error {
	usedLegacy := false

	//nolint:staticcheck // reading the deprecated field is this function's job
	if legacy := options.Network; legacy != "" {
		if options.Transport != "" && options.Transport != legacy {
			return E.New("masque: `transport` is ", options.Transport,
				" but deprecated `network` is ", legacy, " — remove `network`")
		}
		if options.Transport == "" {
			options.Transport = legacy
		}
		usedLegacy = true
	}

	if options.TLS == nil {
		options.TLS = &option.OutboundTLSOptions{}
	}
	tlsOptions := options.TLS

	//nolint:staticcheck
	if legacy := options.SNI; legacy != "" {
		if tlsOptions.ServerName != "" && tlsOptions.ServerName != legacy {
			return E.New("masque: `tls.server_name` is ", tlsOptions.ServerName,
				" but deprecated `sni` is ", legacy, " — remove `sni`")
		}
		if tlsOptions.ServerName == "" {
			tlsOptions.ServerName = legacy
		}
		usedLegacy = true
	}

	// The bool fields below cannot tell "absent" from an explicit false, so a
	// legacy true is carried over and a legacy false says nothing. There is no
	// conflict to detect: with no way to read an explicit `tls.insecure: false`,
	// rejecting the pair would also reject the ordinary config that spells the
	// default out. A legacy true simply wins, which is the safe direction — it
	// preserves what the old config asked for.
	//nolint:staticcheck
	if options.SkipCertVerify {
		tlsOptions.Insecure = true
		usedLegacy = true
	}

	//nolint:staticcheck
	if options.Fragment {
		tlsOptions.Fragment = true
		usedLegacy = true
	}
	//nolint:staticcheck
	if options.RecordFragment {
		tlsOptions.RecordFragment = true
		usedLegacy = true
	}
	//nolint:staticcheck
	if legacy := options.FragmentFallbackDelay; legacy != badoption.Duration(0) {
		if tlsOptions.FragmentFallbackDelay != badoption.Duration(0) &&
			tlsOptions.FragmentFallbackDelay != legacy {
			return E.New("masque: `tls.fragment_fallback_delay` and deprecated " +
				"`fragment_fallback_delay` disagree — remove the latter")
		}
		if tlsOptions.FragmentFallbackDelay == badoption.Duration(0) {
			tlsOptions.FragmentFallbackDelay = legacy
		}
		usedLegacy = true
	}

	if usedLegacy {
		deprecated.Report(ctx, deprecated.OptionMASQUELegacyFields)
	}
	return nil
}

// warnUnsupportedTLSOptions reports settings that the shared TLS block carries
// but masque cannot act on, so a user who sets them is not left believing they
// took effect. These are warnings, not errors: the config stays valid and the
// behaviour stays predictable.
func warnUnsupportedTLSOptions(ctx context.Context, logger interface {
	WarnContext(ctx context.Context, args ...any)
}, transport string, tlsOptions *option.OutboundTLSOptions,
) {
	if tlsOptions == nil {
		return
	}
	if len(tlsOptions.ALPN) > 0 {
		// ALPN follows from the transport (h3 → "h3", h2 → "h2"); an override
		// would break the very negotiation that picks the tunnel protocol.
		logger.WarnContext(ctx, "masque: `tls.alpn` is ignored — ALPN follows `transport`")
	}
	if tlsOptions.ECH != nil && tlsOptions.ECH.Enabled {
		logger.WarnContext(ctx, "masque: `tls.ech` is ignored — not supported for masque")
	}
	if tlsOptions.Reality != nil && tlsOptions.Reality.Enabled {
		logger.WarnContext(ctx, "masque: `tls.reality` is ignored — not supported for masque")
	}
	if tlsOptions.KernelTx || tlsOptions.KernelRx {
		logger.WarnContext(ctx, "masque: `tls.kernel_tx`/`tls.kernel_rx` are ignored — not supported for masque")
	}
	if transport == "h3" && (tlsOptions.Fragment || tlsOptions.RecordFragment) {
		// h3 carries TLS inside QUIC, not over TCP: there is no TLS record
		// stream to split. See SPEC 060.
		logger.WarnContext(ctx, "masque: fragmentation is ignored on `transport: h3` — QUIC carries no TLS records over TCP")
	}
}
