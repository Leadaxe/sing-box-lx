package adapter

// lx: SPEC 073 — швы outbound'а `chain`, общие для ядра и наблюдаемости.
//
// Здесь нет логики цепочки — только контекстная привязка «этот дозвон принадлежит
// хопу i цепочки X» и интерфейсы, по которым группы, менеджеры, трекер соединений
// и RPC разговаривают с цепочкой, не импортируя её пакет.

import (
	"context"
	"time"
)

type chainHopKey struct{}

// ChainLeafResolver реализует хоп цепочки: отображает выбранный группой узел на
// его звено для этого хопа (или на прозрачный проход для `direct`).
type ChainLeafResolver interface {
	ResolveLeaf(ctx context.Context, leaf Outbound) (Outbound, error)
}

// ContextWithChainHop помечает ctx привязкой к хопу. Хоп ВСЕГДА перезаписывает
// привязку своей: в него приходят и с пользовательским ctx, и из фонового
// реконнекта звена, и через штатный DetourDialer от звена сверху.
func ContextWithChainHop(ctx context.Context, hop ChainLeafResolver) context.Context {
	return context.WithValue(ctx, chainHopKey{}, hop)
}

// ContextWithoutChainHop стирает привязку — хоп 0 (реальная сеть) передаёт ctx
// дальше без неё, чтобы группа на входе выбирала оригиналы.
func ContextWithoutChainHop(ctx context.Context) context.Context {
	if ctx.Value(chainHopKey{}) == nil {
		return ctx
	}
	return context.WithValue(ctx, chainHopKey{}, (ChainLeafResolver)(nil))
}

// ResolveChainLeaf — хук в точках, где группа дозванивается до выбранного
// участника. Без привязки или для вложенной группы — no-op (возвращает picked);
// иначе — звено для (хоп, picked). Звено несёт тег оригинала, поэтому вся
// логика группы (история, штрафы, sticky, passive-check) продолжает работать.
func ResolveChainLeaf(ctx context.Context, picked Outbound) (Outbound, error) {
	hop, _ := ctx.Value(chainHopKey{}).(ChainLeafResolver)
	if hop == nil || picked == nil {
		return picked, nil
	}
	if _, isGroup := picked.(OutboundGroup); isGroup {
		return picked, nil
	}
	return hop.ResolveLeaf(ctx, picked)
}

// ChainPathProvider отдаёт разрешённый путь цепочки — по одному тегу узла на
// НЕпрозрачную позицию, в порядке пакета (вход первым). Снимок на момент вызова.
type ChainPathProvider interface {
	ChainPath() []string
}

// EndpointCloneHolder — outbound, владеющий рантайм-endpoint'ами, которых нет в
// менеджере endpoint'ов: idle-тик ENERGY (SPEC 020) и смена сети обязаны
// дотягиваться до них тем же обходом, что и до обычных endpoint'ов.
type EndpointCloneHolder interface {
	Tag() string
	CloneEndpoints() []Endpoint
}

// OutboundOptionsProvider / EndpointOptionsProvider — менеджеры, помнящие опции
// созданных outbound'ов/endpoint'ов по тегу. Нужны фабрике звеньев.
type OutboundOptionsProvider interface {
	OptionsOf(tag string) (outboundType string, options any, loaded bool)
}

type EndpointOptionsProvider interface {
	OptionsOf(tag string) (endpointType string, options any, loaded bool)
}

// InternalOutboundRegistrar — регистрация внутренних outbound'ов (хопов цепочки):
// видны Outbound(tag) (для DetourDialer и URLTest по тегу), не входят в
// Outbounds(), не стартуются и не закрываются менеджером — владелец сам.
type InternalOutboundRegistrar interface {
	AddInternal(outbound Outbound) error
	RemoveInternal(tag string)
}

// ChainStatusProvider — состояние цепочки для RPC / Clash API.
type ChainStatusProvider interface {
	ChainStatus() ChainStatus
}

// ChainController — SPEC 075: runtime control surface of a chain outbound.
// SetPositionEnabled toggles one position (packet order, 0 = entry); the flag
// always applies — warmupError reports a failed link warm-up as data, not as an
// error. CloneConfigJSON returns the effective post-transform options JSON of
// the live link at the position's currently resolved leaf.
type ChainController interface {
	SetPositionEnabled(position int, enabled bool) (warmupError string, err error)
	CloneConfigJSON(position int) (string, error)
}

// ChainDisabledStore — SPEC 075: cache-file extension persisting the disabled
// position set per chain tag. Stored as position tags (not indices), so config
// edits keep the right hops disabled; tags no longer present are ignored on
// load. Implemented by the cache-file service; discovered via type assertion
// on adapter.CacheFile so the upstream interface stays untouched.
type ChainDisabledStore interface {
	LoadChainDisabled(chainTag string) []string
	StoreChainDisabled(chainTag string, disabledTags []string) error
}

type ChainStatus struct {
	Tag           string                `json:"tag"`
	Positions     []ChainPositionStatus `json:"positions"`
	Dials         int64                 `json:"dials"`
	Errors        int64                 `json:"errors"`
	ClonesCreated int64                 `json:"clones_created"`
	ClonesEvicted int64                 `json:"clones_evicted"`
	LiveClones    int64                 `json:"live_clones"`
}

type ChainPositionStatus struct {
	Tag         string            `json:"tag"`
	IsGroup     bool              `json:"is_group"`
	Now         string            `json:"now"`
	Transparent bool              `json:"transparent"`
	Disabled    bool              `json:"disabled"` // SPEC 075: runtime toggle
	Errors      int64             `json:"errors"`
	Clone       *ChainCloneStatus `json:"clone,omitempty"`
}

type ChainCloneStatus struct {
	State         string    `json:"state"` // starting | active | idle
	ActiveConns   int64     `json:"active_conns"`
	LastPicked    time.Time `json:"last_picked"`
	CreatedAt     time.Time `json:"created_at"`
	MTUConfigured uint32    `json:"mtu_configured,omitempty"`
	MTUEffective  uint32    `json:"mtu_effective,omitempty"`
	MTUReason     string    `json:"mtu_reason,omitempty"`
	Stripped      []string  `json:"stripped,omitempty"`
	Rewritten     bool      `json:"rewritten"`
	LastError     string    `json:"last_error,omitempty"`
}
