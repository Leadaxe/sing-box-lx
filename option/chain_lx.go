package option

import (
	"context"

	"github.com/sagernet/sing-box/schema"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
)

// ChainOutboundOptions — конфиг outbound'а типа `chain` (lx: SPEC 073 / 165,
// FEATURE 015).
//
// Outbounds — позиции цепочки В ПОРЯДКЕ ПРОХОЖДЕНИЯ ПАКЕТА: [0] — первый хоп от
// клиента (касается реальной сети и используется как есть), последний — узел,
// чей адрес видит цель. Любая позиция — узел, endpoint или группа любой
// вложенности. При загрузке конфига каждый узел позиции ≥ 1 (члены групп
// тоже) получает `detour` на хоп позиции ниже (SPEC 165).
type ChainOutboundOptions struct {
	Outbounds []string `json:"outbounds" reference:"outbound"`
	// Rewrite — JSON merge-patch (RFC 7396) поверх опций узла данного типа;
	// применяется к перехваченным узлам (позиции ≥ 1) до выравнивания MTU.
	Rewrite ChainRewrite `json:"rewrite,omitempty"`
	// InterruptExistConnections — SPEC 075: selector semantics for the runtime
	// position toggle. Internal connections are always interrupted on a toggle;
	// external (user) connections only when this flag is set.
	InterruptExistConnections bool `json:"interrupt_exist_connections,omitempty"`
}

// The keys of the link model (SPEC 073), removed by SPEC 165: any value fails
// the load and says what replaced it (ALGORITHM П7).
var chainRemovedKeys = [...]struct{ key, text string }{
	{"idle_timeout", "chain idle_timeout is removed: a chain no longer creates link instances, every node on a position >= 1 dials through the hop below as itself"},
	{"strip_evasion", "chain strip_evasion is moved to lx.detour.strip"},
	{"strip", "chain strip is moved to lx.detour.strip"},
}

// UnmarshalJSON checks the removed keys by presence, null included.
func (o *ChainOutboundOptions) UnmarshalJSON(content []byte) error {
	var keys map[string]json.RawMessage
	if json.Unmarshal(content, &keys) == nil {
		for _, removed := range chainRemovedKeys {
			if _, present := keys[removed.key]; present {
				return E.New(removed.text)
			}
		}
	}
	return json.UnmarshalDisallowUnknownFields(content, (*chainOutboundOptionsPlain)(o))
}

type chainOutboundOptionsPlain ChainOutboundOptions

// ChainRewrite — карта «тип узла → JSON merge-patch» (см. ChainOutboundOptions.Rewrite).
// Именованный тип нужен генератору `sing-box schema`: значение-патч — произвольный
// объект, а `map[string]any` он описать не умеет.
type ChainRewrite map[string]any

func (r ChainRewrite) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return builder.Define("ChainRewrite", func() (*schema.Node, error) {
		return &schema.Node{Type: "object", AdditionalProperties: schema.LooseObject()}, nil
	})
}

// ChainHopOutboundOptions is the internal hop "<chain>#<i>" (lx: SPEC 165,
// ALGORITHM §3). Only the config rewrite creates it; a user config with the
// type fails the load (К3).
type ChainHopOutboundOptions struct {
	Chain    string `json:"chain"`
	Position int    `json:"position"`
	// Outbound is the tag of position Position.
	Outbound string `json:"outbound"`
	// Below is the hop of position Position-1, empty for the entry.
	Below string `json:"below,omitempty"`
}

// LXChainRewrite is the SPEC 165 rewrite of a config's chains: it replaces
// the outbound and endpoint slices of options, gives every taken-over node a
// fresh options struct with detour on the hop below and appends the hops. It
// returns the changes per node tag.
type LXChainRewrite func(ctx context.Context, options *Options) (map[string][]LXConfigChange, error)

var lxChainRewrite LXChainRewrite

// RegisterLXChainRewrite installs the rewrite; the chain package does it when
// it registers the chain outbound (with_lx_chain builds only).
func RegisterLXChainRewrite(rewrite LXChainRewrite) {
	lxChainRewrite = rewrite
}

// RewriteLXChains runs the registered rewrite; without one (a build without
// with_lx_chain) it changes nothing.
func RewriteLXChains(ctx context.Context, options *Options) (map[string][]LXConfigChange, error) {
	if lxChainRewrite == nil {
		return nil, nil
	}
	return lxChainRewrite(ctx, options)
}
