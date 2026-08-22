package option

import "github.com/sagernet/sing/common/json/badoption"

// ChainOutboundOptions — конфиг outbound'а типа `chain` (lx: SPEC 073 / FEATURE 015).
//
// Outbounds — позиции цепочки В ПОРЯДКЕ ПРОХОЖДЕНИЯ ПАКЕТА: [0] — первый хоп от
// клиента (касается реальной сети и используется как есть), последний — узел,
// чей адрес видит цель. Любая позиция — узел, endpoint или группа любой
// вложенности; группы не копируются, узлы на позициях ≥ 1 получают рантайм-
// экземпляр «узел через предыдущую позицию» (звено).
type ChainOutboundOptions struct {
	Outbounds []string `json:"outbounds" reference:"outbound"`
	// IdleTimeout — простой, после которого звено без живых соединений
	// удаляется. 0 — жить до остановки. По умолчанию 5m.
	IdleTimeout badoption.Duration `json:"idle_timeout,omitempty"`
	// StripEvasion — снимать у звеньев односторонние DPI-приёмы (каталог
	// `strip`). nil/отсутствует = true.
	StripEvasion *bool `json:"strip_evasion,omitempty"`
	// Strip — патч к каталогу поверх StripEvasion: false — не снимать,
	// true — снимать дополнительно. Неизвестный ключ — ошибка старта.
	Strip map[string]bool `json:"strip,omitempty"`
	// Rewrite — JSON merge-patch (RFC 7396) поверх опций узла данного типа;
	// применяется к звеньям (позиции ≥ 1) после strip и до подгонки MTU.
	Rewrite map[string]any `json:"rewrite,omitempty"`
}

// StripEvasionEnabled — дефолт true.
func (o ChainOutboundOptions) StripEvasionEnabled() bool {
	return o.StripEvasion == nil || *o.StripEvasion
}
