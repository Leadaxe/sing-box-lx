package adapter

// lx: SPEC 073 / 165 — швы outbound'а `chain`, общие для ядра и наблюдаемости.
//
// Цепочка — перезапись `detour` при загрузке конфига (SPEC 165): узлы позиций
// ≥ 1 сами ходят через хоп позиции ниже. Здесь остаётся одна контекстная
// привязка — «выбранный группой `direct` = хоп ниже» (ALGORITHM Х8) — и
// интерфейсы, по которым трекер, RPC и Clash API разговаривают с цепочкой, не
// импортируя её пакет.

import "context"

type chainDirectKey struct{}

// ContextWithChainDirect marks ctx: a group on this dial that picks `direct`
// goes through below instead (ALGORITHM Х3, Х8). A hop of position >= 1 always
// overwrites the mark with its own below; the entry hop clears it (below nil).
func ContextWithChainDirect(ctx context.Context, below Outbound) context.Context {
	if below == nil && ctx.Value(chainDirectKey{}) == nil {
		return ctx
	}
	return context.WithValue(ctx, chainDirectKey{}, below)
}

// ResolveChainLeaf — хук в точках, где группа дозванивается до выбранного
// участника; дозвон идёт возвращённым контекстом. Записывает носителя
// соединения (SPEC 060, ALGORITHM Х7) и, если дозвон идёт через хоп позиции
// ≥ 1, подменяет выбранный `direct` хопом ниже (Х8). Других подмен нет:
// выбранный узел идёт сам, своим detour, и без метки — его собственные
// дозвоны (DNS своего сервера) к цепочке не относятся. Метка остаётся только
// для вложенной группы.
func ResolveChainLeaf(ctx context.Context, picked Outbound) (context.Context, Outbound, error) {
	if picked == nil {
		return ctx, picked, nil
	}
	if picked.Type() == chainTypeDirect {
		if below, _ := ctx.Value(chainDirectKey{}).(Outbound); below != nil {
			return ctx, below, nil
		}
	}
	RecordCarrier(ctx, picked) // lx: SPEC 060 — the member the group picked carries the dial
	if _, isGroup := picked.(OutboundGroup); isGroup {
		return ctx, picked, nil
	}
	return ContextWithChainDirect(ctx, nil), picked, nil
}

// The type names the seams compare with; constant imports adapter-free
// packages only, so the two literals live here.
const (
	chainTypeDirect = "direct"
	chainTypeHop    = "chain-hop"
)

// ChainPathProvider отдаёт разрешённый путь цепочки — по одному тегу узла на
// НЕпрозрачную позицию, в порядке пакета (вход первым). Снимок на момент вызова.
type ChainPathProvider interface {
	ChainPath() []string
}

// ChainStatusProvider — состояние цепочки для RPC / Clash API.
type ChainStatusProvider interface {
	ChainStatus() ChainStatus
}

// ChainController — SPEC 075: runtime control surface of a chain outbound.
// SetPositionEnabled toggles one position (packet order, 0 = entry).
type ChainController interface {
	SetPositionEnabled(position int, enabled bool) error
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

// ChainStatus — позиции цепочки, выбранный узел на каждой, выключение и
// счётчики ошибок (SPEC 165: полей звеньев больше нет).
type ChainStatus struct {
	Tag       string                `json:"tag"`
	Positions []ChainPositionStatus `json:"positions"`
	Dials     int64                 `json:"dials"`
	Errors    int64                 `json:"errors"`
}

type ChainPositionStatus struct {
	Tag         string `json:"tag"`
	IsGroup     bool   `json:"is_group"`
	Now         string `json:"now"`
	Transparent bool   `json:"transparent"`
	Disabled    bool   `json:"disabled"` // SPEC 075: runtime toggle
	Errors      int64  `json:"errors"`
}
