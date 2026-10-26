package libbox

// lx: SPEC 139 — gomobile view of StartConnectionTest: the request builder, the
// stage/finding/result objects and the handler. Everything crosses the bridge as
// objects with getters and iterators: gomobile binds neither slices, maps nor
// nested request structs, and uint32 is converted to int32.

import "github.com/sagernet/sing-box/daemon"

// Test depth, for ConnectionTestRequest.SetDepth.
const (
	ConnectionTestDepthHTTP    int32 = int32(daemon.ConnectionTestDepth_DEPTH_HTTP)
	ConnectionTestDepthDNS     int32 = int32(daemon.ConnectionTestDepth_DEPTH_DNS)
	ConnectionTestDepthRoute   int32 = int32(daemon.ConnectionTestDepth_DEPTH_ROUTE)
	ConnectionTestDepthConnect int32 = int32(daemon.ConnectionTestDepth_DEPTH_CONNECT)
)

// Stage results, as returned by ConnectionTestStage.Result.
const (
	ConnectionTestStageRunning      int32 = int32(daemon.ConnectionTestStageResult_STAGE_RUNNING)
	ConnectionTestStageOK           int32 = int32(daemon.ConnectionTestStageResult_STAGE_OK)
	ConnectionTestStageFailed       int32 = int32(daemon.ConnectionTestStageResult_STAGE_FAILED)
	ConnectionTestStageSkipped      int32 = int32(daemon.ConnectionTestStageResult_STAGE_SKIPPED)
	ConnectionTestStageReused       int32 = int32(daemon.ConnectionTestStageResult_STAGE_REUSED)
	ConnectionTestStageNoAck        int32 = int32(daemon.ConnectionTestStageResult_STAGE_NO_ACK)
	ConnectionTestStageNotEvaluated int32 = int32(daemon.ConnectionTestStageResult_STAGE_NOT_EVALUATED)
)

// Finding levels, as returned by ConnectionTestFinding.Level.
const (
	ConnectionTestFindingInfo    int32 = int32(daemon.ConnectionTestFindingLevel_FINDING_INFO)
	ConnectionTestFindingWarning int32 = int32(daemon.ConnectionTestFindingLevel_FINDING_WARNING)
	ConnectionTestFindingProblem int32 = int32(daemon.ConnectionTestFindingLevel_FINDING_PROBLEM)
)

// ConnectionTestRequest describes one connection test. Every field is optional
// except the link; an empty outbound tag tests through the router.
type ConnectionTestRequest struct {
	link        string
	outboundTag string
	source      connectionTestSource
	appDNS      bool
	connectIP   string
	fresh       bool
	depth       int32
	network     string
	timeout     int32
	maxBytes    int32
	headers     []*daemon.HttpHeaderPair
}

// connectionTestSource mirrors daemon.ConnectionTestSource without its protoimpl
// state, so the request can be copied and compared.
type connectionTestSource struct {
	Inbound     string
	PackageName string
	Uid         int32
	ProcessPath string
	SourceIP    string
	SourceMAC   string
	AuthUser    string
}

func NewConnectionTestRequest() *ConnectionTestRequest {
	return new(ConnectionTestRequest)
}

// SetLink sets the http/https URL to fetch.
func (r *ConnectionTestRequest) SetLink(link string) { r.link = link }

// SetOutboundTag tests directly through one node or endpoint instead of the router.
func (r *ConnectionTestRequest) SetOutboundTag(tag string) { r.outboundTag = tag }

// SetInbound sets the inbound tag the test connection enters through.
func (r *ConnectionTestRequest) SetInbound(tag string) { r.source.Inbound = tag }

// SetPackageName sets the Android package the test runs on behalf of.
func (r *ConnectionTestRequest) SetPackageName(name string) { r.source.PackageName = name }

// SetUID sets the source user id; 0 means not set.
func (r *ConnectionTestRequest) SetUID(uid int32) { r.source.Uid = uid }

// SetProcessPath sets the desktop process path the test runs on behalf of.
func (r *ConnectionTestRequest) SetProcessPath(path string) { r.source.ProcessPath = path }

// SetSourceIP sets the source address, e.g. a LAN device behind a router.
func (r *ConnectionTestRequest) SetSourceIP(ip string) { r.source.SourceIP = ip }

// SetSourceMAC sets the source MAC address.
func (r *ConnectionTestRequest) SetSourceMAC(mac string) { r.source.SourceMAC = mac }

// SetAuthUser sets the authenticated inbound user.
func (r *ConnectionTestRequest) SetAuthUser(user string) { r.source.AuthUser = user }

// SetAppDNS makes the test first issue the DNS queries the app would issue.
func (r *ConnectionTestRequest) SetAppDNS(enabled bool) { r.appDNS = enabled }

// SetConnectIP connects to this address instead of the DNS answer.
func (r *ConnectionTestRequest) SetConnectIP(ip string) { r.connectIP = ip }

// SetFresh bypasses caches and live connections where the protocol allows it.
func (r *ConnectionTestRequest) SetFresh(fresh bool) { r.fresh = fresh }

// SetDepth sets how far the test goes, one of the ConnectionTestDepth* constants.
func (r *ConnectionTestRequest) SetDepth(depth int32) { r.depth = depth }

// SetNetwork sets "tcp" (default) or "udp".
func (r *ConnectionTestRequest) SetNetwork(network string) { r.network = network }

// SetTimeout sets the overall timeout in milliseconds; 0 means the core default.
func (r *ConnectionTestRequest) SetTimeout(ms int32) { r.timeout = ms }

// SetMaxBytes clamps the response body; 0 means the core default.
func (r *ConnectionTestRequest) SetMaxBytes(n int32) { r.maxBytes = n }

// SetHeaders sets the request headers; nil clears them.
func (r *ConnectionTestRequest) SetHeaders(headers *HTTPHeaders) {
	if headers == nil {
		r.headers = nil
		return
	}
	r.headers = append([]*daemon.HttpHeaderPair(nil), headers.pairs...)
}

func (r *ConnectionTestRequest) toGRPC() *daemon.ConnectionTestRequest {
	request := &daemon.ConnectionTestRequest{
		Link:        r.link,
		OutboundTag: r.outboundTag,
		AppDNS:      r.appDNS,
		ConnectIP:   r.connectIP,
		Fresh:       r.fresh,
		Depth:       daemon.ConnectionTestDepth(r.depth),
		Network:     r.network,
		Timeout:     uint32(max(r.timeout, 0)),
		MaxBytes:    uint32(max(r.maxBytes, 0)),
		Headers:     r.headers,
	}
	if source := r.source; source != (connectionTestSource{}) {
		request.Source = &daemon.ConnectionTestSource{
			Inbound:     source.Inbound,
			PackageName: source.PackageName,
			Uid:         source.Uid,
			ProcessPath: source.ProcessPath,
			SourceIP:    source.SourceIP,
			SourceMAC:   source.SourceMAC,
			AuthUser:    source.AuthUser,
		}
	}
	return request
}

// ConnectionTestAttr is one key/value attribute of a stage or finding. The
// vocabulary is open: render unknown keys as they are.
type ConnectionTestAttr struct {
	key   string
	value string
}

func (a *ConnectionTestAttr) Key() string   { return a.key }
func (a *ConnectionTestAttr) Value() string { return a.value }

type ConnectionTestAttrIterator interface {
	Len() int32
	HasNext() bool
	Next() *ConnectionTestAttr
}

// ConnectionTestStage is one step of the pipeline. The same ID arrives first as
// running and again on completion; Parent 0 is a top-level stage.
type ConnectionTestStage struct {
	id         int32
	parent     int32
	kind       string
	subject    string
	startMs    int32
	durationMs int32
	result     int32
	err        string
	attrs      []*ConnectionTestAttr
}

func (s *ConnectionTestStage) ID() int32         { return s.id }
func (s *ConnectionTestStage) Parent() int32     { return s.parent }
func (s *ConnectionTestStage) Kind() string      { return s.kind }
func (s *ConnectionTestStage) Subject() string   { return s.subject }
func (s *ConnectionTestStage) StartMs() int32    { return s.startMs }
func (s *ConnectionTestStage) DurationMs() int32 { return s.durationMs }

// Result returns one of the ConnectionTestStage* constants.
func (s *ConnectionTestStage) Result() int32 { return s.result }

// ErrorMessage returns the cause as is; empty unless the stage failed. Not named
// Error: that would make the type a Go error and a Java Exception under gomobile.
func (s *ConnectionTestStage) ErrorMessage() string { return s.err }

func (s *ConnectionTestStage) Attrs() ConnectionTestAttrIterator {
	return newIterator(s.attrs)
}

// ConnectionTestFinding is a code computed by the core from the trace; the
// client writes the text.
type ConnectionTestFinding struct {
	code   string
	level  int32
	stages []int32
	attrs  []*ConnectionTestAttr
}

func (f *ConnectionTestFinding) Code() string { return f.code }

// Level returns one of the ConnectionTestFinding* constants.
func (f *ConnectionTestFinding) Level() int32 { return f.level }

// Stages returns the IDs of the stages the finding refers to.
func (f *ConnectionTestFinding) Stages() Int32Iterator {
	return newIterator(f.stages)
}

func (f *ConnectionTestFinding) Attrs() ConnectionTestAttrIterator {
	return newIterator(f.attrs)
}

type ConnectionTestFindingIterator interface {
	Len() int32
	HasNext() bool
	Next() *ConnectionTestFinding
}

// ConnectionTestResult is the last event of a test. A non-empty ErrorMessage is
// an error of the test itself (invalid request, busy): it arrives here, not in
// ConnectionTestHandler.OnError.
type ConnectionTestResult struct {
	err             string
	rootCause       int32
	findings        []*ConnectionTestFinding
	httpStatus      int32
	body            []byte
	truncated       bool
	contentType     string
	elapsedMs       int32
	stagesTruncated bool
}

// ErrorMessage returns the error of the test itself; empty when the test ran.
func (r *ConnectionTestResult) ErrorMessage() string { return r.err }

// RootCause returns the stage ID of the root cause; 0 when the path was completed.
func (r *ConnectionTestResult) RootCause() int32 { return r.rootCause }

func (r *ConnectionTestResult) Findings() ConnectionTestFindingIterator {
	return newIterator(r.findings)
}

func (r *ConnectionTestResult) HTTPStatus() int32   { return r.httpStatus }
func (r *ConnectionTestResult) Body() []byte        { return r.body }
func (r *ConnectionTestResult) Truncated() bool     { return r.truncated }
func (r *ConnectionTestResult) ContentType() string { return r.contentType }
func (r *ConnectionTestResult) ElapsedMs() int32    { return r.elapsedMs }

// StagesTruncated reports that the stage cap was hit and the tree is incomplete.
func (r *ConnectionTestResult) StagesTruncated() bool { return r.stagesTruncated }

// ConnectionTestHandler receives the events of one test. OnError fires only on a
// transport failure of the stream; an error of the test itself arrives in OnResult.
type ConnectionTestHandler interface {
	OnStage(stage *ConnectionTestStage)
	OnResult(result *ConnectionTestResult)
	OnError(message string)
}

// ConnectionTestSession is a running test. Cancel stops it without waiting and is
// safe to call from a handler callback; Close stops it and waits for the stream.
type ConnectionTestSession struct {
	streamSession
}

func (s *ConnectionTestSession) Cancel() {
	s.cancel()
}

func connectionTestAttrsFromGRPC(attrs []*daemon.ConnectionTestAttr) []*ConnectionTestAttr {
	if len(attrs) == 0 {
		return nil
	}
	result := make([]*ConnectionTestAttr, 0, len(attrs))
	for _, attr := range attrs {
		result = append(result, &ConnectionTestAttr{key: attr.GetKey(), value: attr.GetValue()})
	}
	return result
}

func connectionTestStageFromGRPC(stage *daemon.ConnectionTestStage) *ConnectionTestStage {
	return &ConnectionTestStage{
		id:         int32(stage.GetId()),
		parent:     int32(stage.GetParent()),
		kind:       stage.GetKind(),
		subject:    stage.GetSubject(),
		startMs:    int32(stage.GetStartMs()),
		durationMs: int32(stage.GetDurationMs()),
		result:     int32(stage.GetResult()),
		err:        stage.GetError(),
		attrs:      connectionTestAttrsFromGRPC(stage.GetAttrs()),
	}
}

func connectionTestFindingFromGRPC(finding *daemon.ConnectionTestFinding) *ConnectionTestFinding {
	var stages []int32
	if len(finding.GetStages()) > 0 {
		stages = make([]int32, 0, len(finding.GetStages()))
		for _, id := range finding.GetStages() {
			stages = append(stages, int32(id))
		}
	}
	return &ConnectionTestFinding{
		code:   finding.GetCode(),
		level:  int32(finding.GetLevel()),
		stages: stages,
		attrs:  connectionTestAttrsFromGRPC(finding.GetAttrs()),
	}
}

func connectionTestResultFromGRPC(result *daemon.ConnectionTestResult) *ConnectionTestResult {
	var findings []*ConnectionTestFinding
	if len(result.GetFindings()) > 0 {
		findings = make([]*ConnectionTestFinding, 0, len(result.GetFindings()))
		for _, finding := range result.GetFindings() {
			findings = append(findings, connectionTestFindingFromGRPC(finding))
		}
	}
	return &ConnectionTestResult{
		err:             result.GetError(),
		rootCause:       int32(result.GetRootCause()),
		findings:        findings,
		httpStatus:      int32(result.GetHttpStatus()),
		body:            result.GetBody(),
		truncated:       result.GetTruncated(),
		contentType:     result.GetContentType(),
		elapsedMs:       int32(result.GetElapsedMs()),
		stagesTruncated: result.GetStagesTruncated(),
	}
}
