//go:build !with_lx_command

package lxtrace

import (
	"context"
	"errors"

	"github.com/sagernet/sing-box/adapter"
)

// Build-tag twin of trace_lx.go (SPEC 139, CONSTITUTION §3.2): the same
// exported surface with no behaviour, so the `// lx:` insertions in upstream
// files compile in a build without with_lx_command and cost nothing there.

type Result int

const (
	Running Result = iota
	OK
	Failed
	Skipped
	Reused
	NoAck
	NotEvaluated
)

type Depth int

const (
	DepthHTTP Depth = iota
	DepthDNS
	DepthRoute
	DepthConnect
)

type Attr struct {
	Key   string
	Value string
}

type StageSnapshot struct {
	ID         uint32
	Parent     uint32
	Kind       string
	Subject    string
	StartMs    uint32
	DurationMs uint32
	Result     Result
	Error      string
	Attrs      []Attr
}

type Summary struct {
	Stages    []StageSnapshot
	RootCause uint32
	Truncated bool
	Dropped   []StageSnapshot
}

type Options struct {
	Fresh     bool
	Probe     bool
	Depth     Depth
	MaxStages int
	Emit      func(StageSnapshot)
	Outbounds adapter.OutboundManager
}

const DefaultMaxStages = 512

var ErrResolveCycle = errors.New("resolve cycle")

type Trace struct{}

type Stage struct{}

func New(opts Options) *Trace { return &Trace{} }

func With(ctx context.Context, t *Trace) context.Context { return ctx }

func From(ctx context.Context) *Trace { return nil }

func Current(ctx context.Context) *Stage { return nil }

func WithStage(ctx context.Context, s *Stage) context.Context { return ctx }

func (t *Trace) Stage(id uint32) *Stage { return nil }

func Begin(ctx context.Context, kind, subject string) (context.Context, *Stage) { return ctx, nil }

func BeginLookup(ctx context.Context, resolver, domain string) (context.Context, *Stage, error) {
	return ctx, nil, nil
}

func (s *Stage) ID() uint32 { return 0 }

func (s *Stage) Attr(key, value string) {}

func (s *Stage) End(err error) {}

func (s *Stage) Mark(result Result) {}

func (s *Stage) LostRace(winner *Stage) {}

func (s *Stage) Children() []StageSnapshot { return nil }

func Fresh(ctx context.Context) bool { return false }

func IsProbe(ctx context.Context) bool { return false }

func DepthOf(ctx context.Context) Depth { return DepthHTTP }

func OutboundsOf(ctx context.Context) adapter.OutboundManager { return nil }

func Routed(ctx context.Context, metadata *adapter.InboundContext, rule adapter.Rule, outbound adapter.Outbound) bool {
	return false
}

func (t *Trace) Finish(cause error) Summary { return Summary{} }

func (t *Trace) RouteRecorded() bool { return false }
