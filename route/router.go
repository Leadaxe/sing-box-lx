package route

import (
	"context"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/process"
	"github.com/sagernet/sing-box/common/taskmonitor"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	R "github.com/sagernet/sing-box/route/rule"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/task"
	"github.com/sagernet/sing/common/x/list"
	"github.com/sagernet/sing/contrab/freelru"
	"github.com/sagernet/sing/contrab/maphash"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

var _ adapter.Router = (*Router)(nil)
var _ adapter.ReachabilityInvalidator = (*Router)(nil) // lx: SPEC 020 idle-suspend

type Router struct {
	ctx               context.Context
	logger            log.ContextLogger
	inbound           adapter.InboundManager
	outbound          adapter.OutboundManager
	dns               adapter.DNSRouter
	dnsTransport      adapter.DNSTransportManager
	connection        adapter.ConnectionManager
	network           adapter.NetworkManager
	httpClientManager adapter.HTTPClientManager
	rules             []adapter.Rule
	needFindProcess   bool
	needFindNeighbor  bool
	leaseFiles        []string
	ruleSets          []adapter.RuleSet
	ruleSetMap        map[string]adapter.RuleSet
	ruleSetUpdater    *R.RuleSetUpdater
	processSearcher   process.Searcher
	processCache      *freelru.Cache[processCacheKey, processCacheEntry]
	neighborResolver  adapter.NeighborResolver
	pauseManager      pause.Manager
	trackers          []adapter.ConnectionTracker
	platformInterface adapter.PlatformInterface
	started           bool
	// lx:begin idle-suspend
	// SPEC 020. idleSuspend is the configured threshold (0 = feature off). idleStop
	// is closed by Close() to stop the idle tick goroutine. reachCache holds the
	// event-driven reachable set (recomputed only when reachDirty is set by
	// InvalidateReachability — selector switch / urltest auto-switch / pool rebuild
	// / reload); reachMu guards publishing it. reachDirty starts true so the first
	// tick computes it. endpoint is the endpoint manager: WG/AWG endpoints live
	// there (NOT in the outbound manager — outbound.Outbounds() never lists them),
	// so the idle tick must iterate it to find IdleSuspendable endpoints.
	idleSuspend          time.Duration
	idleSuspendReachable time.Duration
	idleTeardown         time.Duration
	idleStop             chan struct{}
	idlePauseCallback    *list.Element[pause.Callback]
	reachMu              sync.RWMutex
	reachCache           map[string]bool
	reachDirty           atomic.Bool
	endpoint             adapter.EndpointManager
	// lx:end idle-suspend
}

func NewRouter(ctx context.Context, logFactory log.Factory, options option.RouteOptions, dnsOptions option.DNSOptions) *Router {
	router := &Router{
		ctx:                  ctx,
		logger:               logFactory.NewLogger("router"),
		inbound:              service.FromContext[adapter.InboundManager](ctx),
		outbound:             service.FromContext[adapter.OutboundManager](ctx),
		dns:                  service.FromContext[adapter.DNSRouter](ctx),
		dnsTransport:         service.FromContext[adapter.DNSTransportManager](ctx),
		connection:           service.FromContext[adapter.ConnectionManager](ctx),
		network:              service.FromContext[adapter.NetworkManager](ctx),
		httpClientManager:    service.FromContext[adapter.HTTPClientManager](ctx),
		rules:                make([]adapter.Rule, 0, len(options.Rules)),
		ruleSetMap:           make(map[string]adapter.RuleSet),
		needFindProcess:      hasRule(options.Rules, isProcessRule) || hasDNSRule(dnsOptions.Rules, isProcessDNSRule) || options.FindProcess,
		needFindNeighbor:     hasRule(options.Rules, isNeighborRule) || hasDNSRule(dnsOptions.Rules, isNeighborDNSRule) || hasLocalNeighborDNSServer(dnsOptions.Servers) || options.FindNeighbor,
		leaseFiles:           options.DHCPLeaseFiles,
		pauseManager:         service.FromContext[pause.Manager](ctx),
		platformInterface:    service.FromContext[adapter.PlatformInterface](ctx),
		idleSuspend:          time.Duration(options.LXIdleSuspend),              // lx: SPEC 020 (0 = off)
		idleSuspendReachable: time.Duration(options.LXIdleSuspendReachable),     // lx: SPEC 020 (0 = reachable never suspends)
		idleTeardown:         idleTeardownOf(options),                           // lx: SPEC 020 level 3 (default = reachable window)
		endpoint:             service.FromContext[adapter.EndpointManager](ctx), // lx: SPEC 020 — idle tick iterates endpoints
	}
	router.reachDirty.Store(true) // lx: SPEC 020 — first tick computes the reachable set
	return router
}

// lx:begin idle-suspend
// idleTeardownOf resolves the level-3 window (SPEC 020): an explicit
// lx_idle_teardown wins; absent, it defaults to lx_idle_suspend_reachable (so a
// config that opted into sleeping reachable endpoints also reclaims their
// netstack after the same window of sleep). An explicit "0" is indistinguishable
// from absent in badoption.Duration, which is why the default is a sane window
// rather than "off" — teardown itself is gated on lx_idle_suspend being set.
func idleTeardownOf(options option.RouteOptions) time.Duration {
	if teardown := time.Duration(options.LXIdleTeardown); teardown > 0 {
		return teardown
	}
	return time.Duration(options.LXIdleSuspendReachable)
}

// lx:end idle-suspend

func (r *Router) Initialize(rules []option.Rule, ruleSets []option.RuleSet) error {
	for i, options := range rules {
		err := R.ValidateNoNestedRuleActions(options)
		if err != nil {
			return E.Cause(err, "parse rule[", i, "]")
		}
		rule, err := R.NewRule(r.ctx, r.logger, options, false)
		if err != nil {
			return E.Cause(err, "parse rule[", i, "]")
		}
		r.rules = append(r.rules, rule)
	}
	for i, options := range ruleSets {
		for _, tag := range options.Tag {
			if _, exists := r.ruleSetMap[tag]; exists {
				return E.New("duplicate rule-set tag: ", tag)
			}
			ruleSet, err := R.NewRuleSet(r.ctx, r.logger, tag, options)
			if err != nil {
				return E.Cause(err, "parse rule-set[", i, "]")
			}
			r.ruleSets = append(r.ruleSets, ruleSet)
			r.ruleSetMap[tag] = ruleSet
		}
	}
	return nil
}

func (r *Router) Start(stage adapter.StartStage) error {
	monitor := taskmonitor.New(r.logger, C.StartTimeout)
	switch stage {
	case adapter.StartStateInitialize:
		if r.needFindNeighbor {
			if r.platformInterface != nil && r.platformInterface.UsePlatformNeighborResolver() {
				monitor.Start("initialize neighbor resolver")
				resolver := newPlatformNeighborResolver(r.logger, r.platformInterface)
				err := resolver.Start()
				monitor.Finish()
				if err != nil {
					r.logger.Error(E.Cause(err, "start neighbor resolver"))
				} else {
					r.neighborResolver = resolver
				}
			} else {
				monitor.Start("initialize neighbor resolver")
				resolver, err := newNeighborResolver(r.logger, r.leaseFiles)
				monitor.Finish()
				if err != nil {
					if err != os.ErrInvalid {
						r.logger.Error(E.Cause(err, "create neighbor resolver"))
					}
				} else {
					err = resolver.Start()
					if err != nil {
						r.logger.Error(E.Cause(err, "start neighbor resolver"))
					} else {
						r.neighborResolver = resolver
					}
				}
			}
		}
	case adapter.StartStateStart:
		var startContext *adapter.HTTPStartContext
		if len(r.ruleSets) > 0 {
			monitor.Start("initialize rule-set")
			startContext = adapter.NewHTTPStartContext()
			var ruleSetStartGroup task.Group
			for i, ruleSet := range r.ruleSets {
				ruleSetInPlace := ruleSet
				ruleSetStartGroup.Append0(func(ctx context.Context) error {
					err := ruleSetInPlace.StartContext(ctx, startContext)
					if err != nil {
						return E.Cause(err, "initialize rule-set[", i, "]")
					}
					return nil
				})
			}
			ruleSetStartGroup.Concurrency(5)
			ruleSetStartGroup.FastFail()
			err := ruleSetStartGroup.Run(r.ctx)
			monitor.Finish()
			if err != nil {
				return err
			}
		}
		if startContext != nil {
			startContext.Close()
		}
		r.ruleSetUpdater = R.NewRuleSetUpdater(r.ctx, r.ruleSets)
		r.network.Initialize(r.ruleSets)
		needFindProcess := r.needFindProcess
		for _, ruleSet := range r.ruleSets {
			metadata := ruleSet.Metadata()
			if metadata.ContainsProcessRule {
				needFindProcess = true
			}
		}
		if C.IsAndroid && r.platformInterface != nil {
			needFindProcess = true
		}
		r.needFindProcess = needFindProcess
		if needFindProcess {
			if r.platformInterface != nil && r.platformInterface.UsePlatformConnectionOwnerFinder() {
				r.processSearcher = newPlatformSearcher(r.platformInterface)
			} else {
				monitor.Start("initialize process searcher")
				searcher, err := process.NewSearcher(process.Config{
					Logger:         r.logger,
					PackageManager: r.network.PackageManager(),
				})
				monitor.Finish()
				if err != nil {
					if err != os.ErrInvalid {
						r.logger.Warn(E.Cause(err, "create process searcher"))
					}
				} else {
					r.processSearcher = searcher
				}
			}
		}
		if r.processSearcher != nil {
			processCache := common.Must1(freelru.New[processCacheKey, processCacheEntry](256, maphash.NewHasher[processCacheKey]().Hash32, true))
			processCache.SetLifetime(200 * time.Millisecond)
			r.processCache = processCache
		}
	case adapter.StartStatePostStart:
		for i, rule := range r.rules {
			monitor.Start("initialize rule[", i, "]")
			err := rule.Start()
			monitor.Finish()
			if err != nil {
				return E.Cause(err, "initialize rule[", i, "]")
			}
		}
		if r.ruleSetUpdater != nil {
			r.ruleSetUpdater.Start()
		}
		r.started = true
		// lx: SPEC 020 — start the idle-suspend tick (with_lx_idle_suspend); the
		// no-tag stub errors here if lx_idle_suspend is set without the build tag.
		return r.startIdleSuspend()
	case adapter.StartStateStarted:
		for _, ruleSet := range r.ruleSets {
			ruleSet.Cleanup()
		}
		runtime.GC()
	}
	return nil
}

func (r *Router) Close() error {
	monitor := taskmonitor.New(r.logger, C.StopTimeout)
	var err error
	r.stopIdleSuspend() // lx: SPEC 020 — stop the idle tick before tearing down
	if r.neighborResolver != nil {
		monitor.Start("close neighbor resolver")
		err = E.Append(err, r.neighborResolver.Close(), func(closeErr error) error {
			return E.Cause(closeErr, "close neighbor resolver")
		})
		monitor.Finish()
	}
	for i, rule := range r.rules {
		monitor.Start("close rule[", i, "]")
		err = E.Append(err, rule.Close(), func(err error) error {
			return E.Cause(err, "close rule[", i, "]")
		})
		monitor.Finish()
	}
	if r.ruleSetUpdater != nil {
		monitor.Start("close rule-set updater")
		err = E.Append(err, r.ruleSetUpdater.Close(), func(err error) error {
			return E.Cause(err, "close rule-set updater")
		})
		monitor.Finish()
	}
	for i, ruleSet := range r.ruleSets {
		monitor.Start("close rule-set[", i, "]")
		err = E.Append(err, ruleSet.Close(), func(err error) error {
			return E.Cause(err, "close rule-set[", i, "]")
		})
		monitor.Finish()
	}
	if r.processSearcher != nil {
		monitor.Start("close process searcher")
		err = E.Append(err, r.processSearcher.Close(), func(err error) error {
			return E.Cause(err, "close process searcher")
		})
		monitor.Finish()
	}
	return err
}

func (r *Router) RuleSet(tag string) (adapter.RuleSet, bool) {
	ruleSet, loaded := r.ruleSetMap[tag]
	return ruleSet, loaded
}

func (r *Router) Rules() []adapter.Rule {
	return r.rules
}

func (r *Router) AppendTracker(tracker adapter.ConnectionTracker) {
	r.trackers = append(r.trackers, tracker)
}

func (r *Router) NeedFindProcess() bool {
	return r.needFindProcess
}

func (r *Router) NeedFindNeighbor() bool {
	return r.needFindNeighbor
}

func (r *Router) NeighborResolver() adapter.NeighborResolver {
	return r.neighborResolver
}

func (r *Router) ResetNetwork() {
	r.httpClientManager.ResetNetwork()
	r.dns.ResetNetwork()
}
