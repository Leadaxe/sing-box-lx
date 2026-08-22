package group

// lx:begin SPEC 054 penalty failover (least_test)
//
// Реакция least_test-группы на отказы боевых дайлов. До этого группа не
// реагировала на ошибки вообще: выбор оставался прибит к мёртвому узлу до
// хвоста следующего пробного прогона (до interval, по умолчанию 3 мин), и с
// SPEC 052 пользователь получал ошибку за 15с — но группа её не потребляла.
//
// Механика (дизайн владельца, 2026-08-07):
//   - Отказ класса «путь мёртв» (isPathDeadDialError) даёт узлу +1 штраф и
//     ОДИН fallback-дайл через следующего кандидата (кап 2 попытки на дайл).
//     Успех fallback'а переносит на него выбор группы — без Interrupt.
//   - Штраф сбрасывается ТОЛЬКО доказательством жизни: успешный боевой дайл
//     или ответ на пробу в ретесте. Никаких сбросов по времени.
//   - Аварийный режим: у лучшего-по-скорости ≥ penaltyThreshold штрафов →
//     выбор ранжируется двумя уровнями (штрафы ↑, затем задержка ↑) среди
//     узлов с history. Обычно победителей по штрафам несколько — они
//     соревнуются по скорости, как обычный least_test.
//   - Тотальные штрафы (нет кандидата со штрафом < порога) → принудительный
//     force-прогон проб, не чаще раза в penaltyForcedRetestGap, отсчёт от
//     ЗАВЕРШЕНИЯ прошлого прогона (последнего ответа), не от старта.
//   - Пока действует аварийный режим, passive-skip проб отключён (см. urlTest):
//     иначе рабочий запасной пассивно подтверждается, циклы пропускаются, и
//     оштрафованный бывший лучший никогда не получает пробу для сброса.
//
// Ошибки НЕ этого класса штрафа не дают и fallback не запускают: RST означает
// «узел донёс, назначение отказало» — через другой узел будет тот же отказ;
// Canceled — клиент ушёл сам. Причинная неоднозначность ошибок (мёртвый узел
// vs мёртвый сайт) принята: один заблокированный сайт может уронить группу в
// аварийный режим, но режим деградирует мягко (выбор уходит на здоровые узлы,
// всё продолжает работать) и смывается первым же ответившим ретестом.
// UDP штрафов не генерирует (у ListenPacket нет connect-сигнала), но выбором
// пользуется общим. round_robin (balancer != nil) не затронут: у пула своя
// машинерия здоровья (SPEC 019 v2), и его комментарий про «ошибка дайла не
// трогает пул» остаётся в силе.

import (
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

const (
	// penaltyThreshold — штрафы лучшего-по-скорости, включающие аварийный режим.
	// С капом 2 (основной + fallback) это ~3 неудачных пользовательских попытки.
	penaltyThreshold = 3
	// penaltyForcedRetestGap — минимальная пауза между принудительными
	// force-прогонами при тотальных штрафах, от конца прошлого прогона.
	penaltyForcedRetestGap = 2 * time.Minute
)

// isPathDeadDialError отделяет «путь мёртв» (таймаут/недостижимость — штраф и
// fallback) от «назначение отказало» (RST — не штраф) и «клиент ушёл» (Canceled).
// context.DeadlineExceeded — это в том числе 15s-дедлайн SPEC 052.
func isPathDeadDialError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

func (g *URLTestGroup) penaltyOf(tag string) int64 {
	value, ok := g.penalties.Load(tag)
	if !ok {
		return 0
	}
	return value.(*atomic.Int64).Load()
}

func (g *URLTestGroup) penaltyAdd(tag string) {
	value, _ := g.penalties.LoadOrStore(tag, new(atomic.Int64))
	value.(*atomic.Int64).Add(1)
}

// penaltyReset — единственный путь обнуления: доказательство жизни (успешный
// боевой дайл или ответ на пробу).
func (g *URLTestGroup) penaltyReset(tag string) {
	if value, ok := g.penalties.Load(tag); ok {
		value.(*atomic.Int64).Store(0)
	}
}

// fastestByDelay — узел с минимальной задержкой среди имеющих history: чистая
// «скорость», без tolerance-гистерезиса и без привязки к текущему выбору.
func (g *URLTestGroup) fastestByDelay(network string) adapter.Outbound {
	var minDelay uint16
	var minOutbound adapter.Outbound
	for _, detour := range g.outbounds {
		if !supportsNetwork(detour, network) {
			continue
		}
		history := g.history.LoadURLTestHistory(RealTag(detour))
		if history == nil {
			continue
		}
		if minOutbound == nil || history.Delay < minDelay {
			minDelay = history.Delay
			minOutbound = detour
		}
	}
	return minOutbound
}

// penaltyEmergency — аварийный режим: лучший-по-скорости набрал ≥ порога.
func (g *URLTestGroup) penaltyEmergency(network string) bool {
	fastest := g.fastestByDelay(network)
	return fastest != nil && g.penaltyOf(RealTag(fastest)) >= penaltyThreshold
}

// penaltyBest — двухуровневое ранжирование (штрафы ↑, задержка ↑) среди узлов
// с history, минус исключённый тег. nil, если кандидатов нет.
func (g *URLTestGroup) penaltyBest(network string, excludeTag string) adapter.Outbound {
	var best adapter.Outbound
	var bestPenalty int64
	var bestDelay uint16
	for _, detour := range g.outbounds {
		if !supportsNetwork(detour, network) {
			continue
		}
		realTag := RealTag(detour)
		if realTag == excludeTag {
			continue
		}
		history := g.history.LoadURLTestHistory(realTag)
		if history == nil {
			continue
		}
		penalty := g.penaltyOf(realTag)
		if best == nil || penalty < bestPenalty || (penalty == bestPenalty && history.Delay < bestDelay) {
			best = detour
			bestPenalty = penalty
			bestDelay = history.Delay
		}
	}
	return best
}

// selectPenaltyAware — Select с учётом штрафов: в аварийном режиме — двухуровневое
// ранжирование; иначе — апстримный Select как есть. Используется performUpdateCheck.
func (g *URLTestGroup) selectPenaltyAware(network string) (adapter.Outbound, bool) {
	if g.balancer == nil && g.penaltyEmergency(network) {
		if best := g.penaltyBest(network, ""); best != nil {
			return best, true
		}
	}
	return g.Select(network)
}

// pickForDial — выбор узла для боевого дайла: аварийный режим обходит кеш
// selectedOutbound* (он может указывать на оштрафованного лучшего), обычный —
// апстримная семантика (кеш, иначе Select).
func (g *URLTestGroup) pickForDial(network string) adapter.Outbound {
	if g.balancer == nil && g.penaltyEmergency(network) {
		if best := g.penaltyBest(network, ""); best != nil {
			return best
		}
	}
	var cached adapter.Outbound
	switch network {
	case N.NetworkTCP:
		cached = g.selectedOutboundTCP
	case N.NetworkUDP:
		cached = g.selectedOutboundUDP
	}
	if cached != nil {
		return cached
	}
	outbound, _ := g.Select(network)
	return outbound
}

// moveSelection переносит выбор на узел, доказавший жизнь fallback-дайлом.
// Без Interrupt: живые соединения не рвём; дерево достижимости (SPEC 020)
// инвалидируется, как при любом переизборе.
func (g *URLTestGroup) moveSelection(network string, detour adapter.Outbound) {
	g.access.Lock()
	var changed bool
	switch network {
	case N.NetworkTCP:
		if g.selectedOutboundTCP != detour {
			g.selectedOutboundTCP = detour
			changed = true
		}
	case N.NetworkUDP:
		if g.selectedOutboundUDP != detour {
			g.selectedOutboundUDP = detour
			changed = true
		}
	}
	g.access.Unlock()
	if changed {
		g.logger.Info("lx penalty: selection moved to ", detour.Tag(), " (", network, ")")
		invalidateReachability(g.ctx)
	}
}

// penaltyFailoverDial — обработка классифицированного отказа боевого дайла:
// штраф отказавшему, возможный аварийный force-прогон и ОДИН fallback-дайл
// (кап 2 попытки на пользовательский дайл — «обновят страницу, попробуем ещё»).
// Возвращает (conn, узел, true) при успехе fallback'а; иначе (nil, nil, false)
// и вызывающий отдаёт исходную ошибку приложению.
func (g *URLTestGroup) penaltyFailoverDial(ctx context.Context, network string, destination M.Socksaddr, failed adapter.Outbound, dialErr error) (net.Conn, adapter.Outbound, bool) {
	if g.balancer != nil || !isPathDeadDialError(dialErr) {
		return nil, nil, false
	}
	g.penaltyAdd(RealTag(failed))
	g.maybeForceRetest()
	fallback := g.penaltyBest(network, RealTag(failed))
	if fallback == nil {
		return nil, nil, false
	}
	// lx: SPEC 073 — fallback-дайл внутри цепочки тоже идёт в звено, не в оригинал.
	fallbackDialer, err := adapter.ResolveChainLeaf(ctx, fallback)
	if err != nil {
		return nil, nil, false
	}
	conn, err := fallbackDialer.DialContext(ctx, network, destination)
	if err != nil {
		g.logger.Error("lx penalty: fallback via ", fallback.Tag(), ": ", E.Cause(err, "dial"))
		// Симметрия с апстримным поведением least_test для отказавшего дайла.
		g.history.DeleteURLTestHistory(RealTag(fallback))
		if isPathDeadDialError(err) {
			g.penaltyAdd(RealTag(fallback))
			g.maybeForceRetest()
		}
		return nil, nil, false
	}
	g.penaltyReset(RealTag(fallback))
	if g.passiveCheck && network == N.NetworkTCP {
		g.markPassiveAlive(fallback.Tag())
	}
	g.moveSelection(network, fallback)
	return conn, fallback, true
}

// maybeForceRetest — аварийный клапан: если среди кандидатов с history нет ни
// одного со штрафом < порога (умер путь целиком, ранжирование бессмысленно) —
// немедленный force-прогон проб. Уровень-триггер с дельта-лимитом
// penaltyForcedRetestGap от КОНЦА прошлого прогона; параллельные прогоны
// схлопывает существующий checking-CAS. Регулярный тикер при этом продолжает
// свой ритм: у мёртвых узлов history удалена, их перепробуют каждый interval.
func (g *URLTestGroup) maybeForceRetest() {
	if g.balancer != nil || !g.penaltyTotal(N.NetworkTCP) {
		return
	}
	last := g.lastForcedRetest.Load()
	if !last.IsZero() && time.Since(last) < penaltyForcedRetestGap {
		return
	}
	if !g.forcedRetestRunning.CompareAndSwap(false, true) {
		return
	}
	g.logger.Warn("lx penalty: all candidates penalized, forcing a retest")
	go func() {
		g.CheckOutbounds(true)
		// Отсчёт паузы от последнего ответа проб, не от старта прогона: длинный
		// прогон по большой мёртвой группе не должен позволять следующему шторму
		// стартовать впритык к концу этого.
		g.lastForcedRetest.Store(time.Now())
		g.forcedRetestRunning.Store(false)
	}()
}

// penaltyTotal — «тотальные штрафы»: есть кандидаты с history, и все ≥ порога.
func (g *URLTestGroup) penaltyTotal(network string) bool {
	var sawCandidate bool
	for _, detour := range g.outbounds {
		if !supportsNetwork(detour, network) {
			continue
		}
		realTag := RealTag(detour)
		if g.history.LoadURLTestHistory(realTag) == nil {
			continue
		}
		sawCandidate = true
		if g.penaltyOf(realTag) < penaltyThreshold {
			return false
		}
	}
	return sawCandidate
}

func supportsNetwork(detour adapter.Outbound, network string) bool {
	for _, n := range detour.Network() {
		if n == network {
			return true
		}
	}
	return false
}

// lx:end SPEC 054
