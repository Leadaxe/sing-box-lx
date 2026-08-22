package chain

import (
	"context"
	"net"
	"runtime/pprof"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
)

type cloneKey struct {
	position int
	leafTag  string
}

// clone — звено: рантайм-экземпляр узла для позиции, с detour на хоп i−1.
// Несёт тег оригинала, поэтому история/штрафы/sticky групп работают без правок.
type clone struct {
	key      cloneKey
	position int
	inner    adapter.Outbound
	info     cloneInfo

	createdAt  time.Time
	lastPicked atomic.Int64 // unix nanos
	active     atomic.Int64
	lastErr    atomic.Pointer[string]
	closeOnce  sync.Once
}

var _ adapter.Outbound = (*clone)(nil)

func (c *clone) Type() string           { return c.inner.Type() }
func (c *clone) Tag() string            { return c.inner.Tag() }
func (c *clone) Network() []string      { return c.inner.Network() }
func (c *clone) Dependencies() []string { return c.inner.Dependencies() }

func (c *clone) touch() {
	c.lastPicked.Store(time.Now().UnixNano())
}

func (c *clone) setErr(err error) {
	if err == nil {
		return
	}
	message := err.Error()
	c.lastErr.Store(&message)
}

func (c *clone) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	c.touch()
	conn, err := c.inner.DialContext(ctx, network, destination)
	if err != nil {
		c.setErr(err)
		return nil, err
	}
	c.active.Add(1)
	return &countedConn{Conn: conn, release: c.release}, nil
}

func (c *clone) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	c.touch()
	conn, err := c.inner.ListenPacket(ctx, destination)
	if err != nil {
		c.setErr(err)
		return nil, err
	}
	c.active.Add(1)
	return &countedPacketConn{PacketConn: conn, release: c.release}, nil
}

func (c *clone) release() {
	c.active.Add(-1)
}

func (c *clone) close() error {
	var err error
	c.closeOnce.Do(func() {
		err = common.Close(c.inner)
	})
	return err
}

func (c *clone) status() *adapter.ChainCloneStatus {
	status := &adapter.ChainCloneStatus{
		State:         "idle",
		ActiveConns:   c.active.Load(),
		LastPicked:    time.Unix(0, c.lastPicked.Load()),
		CreatedAt:     c.createdAt,
		MTUConfigured: c.info.mtuConfigured,
		MTUEffective:  c.info.mtuEffective,
		MTUReason:     c.info.mtuReason,
		Stripped:      c.info.stripped,
		Rewritten:     c.info.rewritten,
	}
	if status.ActiveConns > 0 {
		status.State = "active"
	}
	if message := c.lastErr.Load(); message != nil {
		status.LastError = *message
	}
	return status
}

// countedConn / countedPacketConn — счётчик живых соединений звена: нулевой
// счётчик — необходимое условие эвикшна. Upstream() сохраняет оптимизации копирования.
type countedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *countedConn) Close() error {
	c.once.Do(c.release)
	return c.Conn.Close()
}

func (c *countedConn) ReaderReplaceable() bool { return true }
func (c *countedConn) WriterReplaceable() bool { return true }
func (c *countedConn) Upstream() any           { return c.Conn }

type countedPacketConn struct {
	net.PacketConn
	release func()
	once    sync.Once
}

func (c *countedPacketConn) Close() error {
	c.once.Do(c.release)
	return c.PacketConn.Close()
}

func (c *countedPacketConn) ReaderReplaceable() bool { return true }
func (c *countedPacketConn) WriterReplaceable() bool { return true }
func (c *countedPacketConn) Upstream() any           { return c.PacketConn }

type cloneCall struct {
	done  chan struct{}
	clone *clone
	err   error
}

// cloneFor — звено для (позиция, узел): из карты, либо создание под
// singleflight по ключу (конкурентные дозвоны ждут первого).
func (c *Chain) cloneFor(position int, leaf adapter.Outbound) (*clone, error) {
	key := cloneKey{position: position, leafTag: leaf.Tag()}
	c.cloneMu.Lock()
	if c.closed {
		c.cloneMu.Unlock()
		return nil, E.New("chain is closed")
	}
	if existing, loaded := c.clones[key]; loaded {
		c.cloneMu.Unlock()
		existing.touch()
		return existing, nil
	}
	if call, inflight := c.inflight[key]; inflight {
		c.cloneMu.Unlock()
		<-call.done
		if call.err != nil {
			return nil, call.err
		}
		call.clone.touch()
		return call.clone, nil
	}
	call := &cloneCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.cloneMu.Unlock()

	created, err := c.createClone(position, leaf)

	c.cloneMu.Lock()
	delete(c.inflight, key)
	if err == nil {
		if c.closed {
			err = E.New("chain is closed")
		} else {
			c.clones[key] = created
		}
	}
	call.clone, call.err = created, err
	c.cloneMu.Unlock()
	close(call.done)
	if err != nil {
		if created != nil {
			created.close()
		}
		c.logger.Error("clone ", leaf.Tag(), "@", position, ": ", err)
		return nil, err
	}
	c.stats.clonesCreated.Add(1)
	c.logger.Info("clone created: ", leaf.Tag(), "@", position, " via ", c.hopTag(position-1), created.info.describe())
	return created, nil
}

// createClone — опции из менеджера → strip → rewrite → MTU → detour → штатный
// реестр → стадии старта, под pprof-метками цепочки/позиции/узла.
func (c *Chain) createClone(position int, leaf adapter.Outbound) (*clone, error) {
	built, err := c.buildCloneOptions(position, leaf)
	if err != nil {
		return nil, err
	}
	loggerTag := "chain[" + c.Tag() + "]#" + strconv.Itoa(position) + "/" + leaf.Tag()
	var cloneLogger log.ContextLogger
	if c.logFactory != nil {
		cloneLogger = c.logFactory.NewLogger(loggerTag)
	} else {
		cloneLogger = c.logger
	}
	var created adapter.Outbound
	labels := pprof.Labels("lx.chain", c.Tag(), "lx.pos", strconv.Itoa(position), "lx.leaf", leaf.Tag())
	pprof.Do(c.ctx, labels, func(ctx context.Context) {
		if built.isEndpoint {
			registry := service.FromContext[adapter.EndpointRegistry](ctx)
			if registry == nil {
				err = E.New("missing endpoint registry")
				return
			}
			created, err = registry.Create(ctx, c.router, cloneLogger, leaf.Tag(), built.typeName, built.options)
		} else {
			registry := service.FromContext[adapter.OutboundRegistry](ctx)
			if registry == nil {
				err = E.New("missing outbound registry")
				return
			}
			created, err = registry.CreateOutbound(ctx, c.router, cloneLogger, leaf.Tag(), built.typeName, built.options)
		}
		if err != nil {
			err = E.Cause(err, "create ", built.typeName, " clone")
			return
		}
		for _, stage := range adapter.ListStartStages {
			err = adapter.LegacyStart(created, stage)
			if err != nil {
				common.Close(created)
				err = E.Cause(err, stage, " clone")
				return
			}
		}
	})
	if err != nil {
		return nil, err
	}
	cl := &clone{
		key:       cloneKey{position: position, leafTag: leaf.Tag()},
		position:  position,
		inner:     created,
		info:      built.info,
		createdAt: time.Now(),
	}
	cl.touch()
	return cl, nil
}

// evictLoop / evictIdle — удаление звеньев по простою: ноль живых соединений И
// без выбора дольше idle_timeout. Переключение группы само по себе звено не
// удаляет: без interrupt_exist_connections старые потоки доживают через него.
func (c *Chain) evictLoop() {
	period := c.idleTimeout / 4
	if period < minEvictTick {
		period = minEvictTick
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.evictIdle(time.Now())
		}
	}
}

func (c *Chain) evictIdle(now time.Time) {
	var victims []*clone
	c.cloneMu.Lock()
	for key, cl := range c.clones {
		if cl.active.Load() != 0 {
			continue
		}
		if now.Sub(time.Unix(0, cl.lastPicked.Load())) <= c.idleTimeout {
			continue
		}
		victims = append(victims, cl)
		delete(c.clones, key)
	}
	c.cloneMu.Unlock()
	for _, cl := range victims {
		if err := cl.close(); err != nil {
			c.logger.Warn("evict clone ", cl.key.leafTag, "@", cl.position, ": ", err)
		} else {
			c.logger.Info("clone evicted: ", cl.key.leafTag, "@", cl.position, " (idle)")
		}
		c.stats.clonesEvicted.Add(1)
	}
}
