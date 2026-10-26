//go:build with_lx_command

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/sagernet/sing-box/daemon"
	E "github.com/sagernet/sing/common/exceptions"

	"github.com/spf13/cobra"
)

// lx: SPEC 139 — active connection test with a stage trace, from StartConnectionTest.

var (
	commandAPIConnectionTestFlagOutbound  string
	commandAPIConnectionTestFlagInbound   string
	commandAPIConnectionTestFlagPackage   string
	commandAPIConnectionTestFlagUID       int32
	commandAPIConnectionTestFlagProcess   string
	commandAPIConnectionTestFlagSourceIP  string
	commandAPIConnectionTestFlagSourceMAC string
	commandAPIConnectionTestFlagUser      string
	commandAPIConnectionTestFlagAppDNS    bool
	commandAPIConnectionTestFlagConnectIP string
	commandAPIConnectionTestFlagFresh     bool
	commandAPIConnectionTestFlagDepth     string
	commandAPIConnectionTestFlagNetwork   string
	commandAPIConnectionTestFlagTimeout   uint32
	commandAPIConnectionTestFlagMaxBytes  uint32
	commandAPIConnectionTestFlagHeaders   []string
)

var commandAPIConnectionTest = &cobra.Command{
	Use:   "connection-test <url>",
	Short: "Run an active connection test and print its stage trace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAPIConnectionTest(args[0])
	},
}

func init() {
	flags := commandAPIConnectionTest.Flags()
	flags.StringVarP(&commandAPIConnectionTestFlagOutbound, "outbound", "o", "", "Test directly through this node or endpoint instead of the router")
	flags.StringVar(&commandAPIConnectionTestFlagInbound, "inbound", "", "Inbound tag the test connection enters through")
	flags.StringVar(&commandAPIConnectionTestFlagPackage, "package", "", "Source package name (Android)")
	flags.Int32Var(&commandAPIConnectionTestFlagUID, "uid", 0, "Source user id")
	flags.StringVar(&commandAPIConnectionTestFlagProcess, "process", "", "Source process path")
	flags.StringVar(&commandAPIConnectionTestFlagSourceIP, "source-ip", "", "Source address")
	flags.StringVar(&commandAPIConnectionTestFlagSourceMAC, "source-mac", "", "Source MAC address")
	flags.StringVar(&commandAPIConnectionTestFlagUser, "user", "", "Authenticated inbound user")
	flags.BoolVar(&commandAPIConnectionTestFlagAppDNS, "app-dns", false, "First issue the DNS queries the app would issue")
	flags.StringVar(&commandAPIConnectionTestFlagConnectIP, "connect-ip", "", "Connect to this address instead of the DNS answer")
	flags.BoolVar(&commandAPIConnectionTestFlagFresh, "fresh", false, "Bypass caches and live connections")
	flags.StringVar(&commandAPIConnectionTestFlagDepth, "depth", "http", "Test depth: http, dns, route or connect")
	flags.StringVar(&commandAPIConnectionTestFlagNetwork, "network", "", "Network: tcp (default) or udp")
	flags.Uint32Var(&commandAPIConnectionTestFlagTimeout, "timeout", 0, "Overall timeout in milliseconds (0: core default)")
	flags.Uint32Var(&commandAPIConnectionTestFlagMaxBytes, "max-bytes", 0, "Response body limit in bytes (0: core default)")
	flags.StringArrayVarP(&commandAPIConnectionTestFlagHeaders, "header", "H", nil, "Request header \"Key: Value\" (repeatable)")
	commandAPIRoot.AddCommand(commandAPIConnectionTest)
}

func connectionTestDepthFromFlag(depth string) (daemon.ConnectionTestDepth, error) {
	switch strings.ToLower(depth) {
	case "", "http":
		return daemon.ConnectionTestDepth_DEPTH_HTTP, nil
	case "dns":
		return daemon.ConnectionTestDepth_DEPTH_DNS, nil
	case "route":
		return daemon.ConnectionTestDepth_DEPTH_ROUTE, nil
	case "connect":
		return daemon.ConnectionTestDepth_DEPTH_CONNECT, nil
	default:
		return 0, E.New("unknown depth: ", depth, " (http, dns, route or connect)")
	}
}

func connectionTestHeadersFromFlag(headers []string) ([]*daemon.HttpHeaderPair, error) {
	var pairs []*daemon.HttpHeaderPair
	for _, header := range headers {
		key, value, found := strings.Cut(header, ":")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			return nil, E.New("invalid header: ", header, " (want \"Key: Value\")")
		}
		pairs = append(pairs, &daemon.HttpHeaderPair{Key: key, Value: strings.TrimSpace(value)})
	}
	return pairs, nil
}

func buildConnectionTestRequest(link string) (*daemon.ConnectionTestRequest, error) {
	depth, err := connectionTestDepthFromFlag(commandAPIConnectionTestFlagDepth)
	if err != nil {
		return nil, err
	}
	headers, err := connectionTestHeadersFromFlag(commandAPIConnectionTestFlagHeaders)
	if err != nil {
		return nil, err
	}
	request := &daemon.ConnectionTestRequest{
		Link:        link,
		OutboundTag: commandAPIConnectionTestFlagOutbound,
		AppDNS:      commandAPIConnectionTestFlagAppDNS,
		ConnectIP:   commandAPIConnectionTestFlagConnectIP,
		Fresh:       commandAPIConnectionTestFlagFresh,
		Depth:       depth,
		Network:     commandAPIConnectionTestFlagNetwork,
		Timeout:     commandAPIConnectionTestFlagTimeout,
		MaxBytes:    commandAPIConnectionTestFlagMaxBytes,
		Headers:     headers,
	}
	source := &daemon.ConnectionTestSource{
		Inbound:     commandAPIConnectionTestFlagInbound,
		PackageName: commandAPIConnectionTestFlagPackage,
		Uid:         commandAPIConnectionTestFlagUID,
		ProcessPath: commandAPIConnectionTestFlagProcess,
		SourceIP:    commandAPIConnectionTestFlagSourceIP,
		SourceMAC:   commandAPIConnectionTestFlagSourceMAC,
		AuthUser:    commandAPIConnectionTestFlagUser,
	}
	if source.Inbound != "" || source.PackageName != "" || source.Uid != 0 || source.ProcessPath != "" ||
		source.SourceIP != "" || source.SourceMAC != "" || source.AuthUser != "" {
		request.Source = source
	}
	return request, nil
}

func runAPIConnectionTest(link string) error {
	request, err := buildConnectionTestRequest(link)
	if err != nil {
		return err
	}
	clientConn, client, err := createAPIClient()
	if err != nil {
		return err
	}
	defer clientConn.Close()
	stream, err := client.StartConnectionTest(globalCtx, request)
	if err != nil {
		return err
	}
	tree := newConnectionTestTree()
	for {
		event, recvErr := stream.Recv()
		if recvErr != nil {
			return recvErr
		}
		if stage := event.GetStage(); stage != nil {
			os.Stdout.WriteString(tree.formatStage(stage) + "\n")
			continue
		}
		if result := event.GetResult(); result != nil {
			os.Stdout.WriteString(tree.formatResult(result))
			if result.GetError() != "" {
				return E.New(result.GetError())
			}
			return nil
		}
	}
}

// connectionTestTree tracks the depth of every stage seen so far, so each line
// can be indented under its parent as stages stream in.
type connectionTestTree struct {
	depth map[uint32]int
	stage map[uint32]*daemon.ConnectionTestStage
}

func newConnectionTestTree() *connectionTestTree {
	return &connectionTestTree{
		depth: make(map[uint32]int),
		stage: make(map[uint32]*daemon.ConnectionTestStage),
	}
}

func connectionTestGlyph(result daemon.ConnectionTestStageResult) string {
	switch result {
	case daemon.ConnectionTestStageResult_STAGE_OK:
		return "✓"
	case daemon.ConnectionTestStageResult_STAGE_FAILED:
		return "✗"
	case daemon.ConnectionTestStageResult_STAGE_RUNNING:
		return "…"
	default:
		return "·"
	}
}

func connectionTestResultName(result daemon.ConnectionTestStageResult) string {
	return strings.ToLower(strings.TrimPrefix(result.String(), "STAGE_"))
}

func formatConnectionTestAttrs(attrs []*daemon.ConnectionTestAttr) string {
	parts := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		parts = append(parts, attr.GetKey()+"="+attr.GetValue())
	}
	return strings.Join(parts, " ")
}

func (t *connectionTestTree) formatStage(stage *daemon.ConnectionTestStage) string {
	depth, known := t.depth[stage.GetId()]
	if !known {
		if stage.GetParent() != 0 {
			depth = t.depth[stage.GetParent()] + 1
		}
		t.depth[stage.GetId()] = depth
	}
	t.stage[stage.GetId()] = stage
	var line strings.Builder
	fmt.Fprintf(&line, "%5d  %s%s %s", stage.GetStartMs(), strings.Repeat("  ", depth), connectionTestGlyph(stage.GetResult()), stage.GetKind())
	if stage.GetSubject() != "" {
		line.WriteString(" " + stage.GetSubject())
	}
	switch stage.GetResult() {
	case daemon.ConnectionTestStageResult_STAGE_OK, daemon.ConnectionTestStageResult_STAGE_FAILED, daemon.ConnectionTestStageResult_STAGE_RUNNING:
	default:
		line.WriteString(" (" + connectionTestResultName(stage.GetResult()) + ")")
	}
	if attrs := formatConnectionTestAttrs(stage.GetAttrs()); attrs != "" {
		line.WriteString("  " + attrs)
	}
	if stage.GetError() != "" {
		line.WriteString("  error: " + stage.GetError())
	}
	if stage.GetResult() != daemon.ConnectionTestStageResult_STAGE_RUNNING {
		fmt.Fprintf(&line, "  %d ms", stage.GetDurationMs())
	}
	return line.String()
}

func (t *connectionTestTree) formatResult(result *daemon.ConnectionTestResult) string {
	var output strings.Builder
	output.WriteString("\n")
	if result.GetError() != "" {
		return output.String()
	}
	if rootCause := result.GetRootCause(); rootCause != 0 {
		fmt.Fprintf(&output, "root cause: stage %d", rootCause)
		if stage := t.stage[rootCause]; stage != nil {
			output.WriteString(" " + stage.GetKind())
			if stage.GetSubject() != "" {
				output.WriteString(" " + stage.GetSubject())
			}
			if stage.GetError() != "" {
				output.WriteString(": " + stage.GetError())
			}
		}
		output.WriteString("\n")
	} else {
		output.WriteString("root cause: none, path completed\n")
	}
	if result.GetHttpStatus() != 0 {
		fmt.Fprintf(&output, "http: %d", result.GetHttpStatus())
		if result.GetContentType() != "" {
			output.WriteString(" " + result.GetContentType())
		}
		fmt.Fprintf(&output, ", %d bytes", len(result.GetBody()))
		if result.GetTruncated() {
			output.WriteString(" (truncated)")
		}
		output.WriteString("\n")
	}
	fmt.Fprintf(&output, "elapsed: %d ms\n", result.GetElapsedMs())
	if result.GetStagesTruncated() {
		output.WriteString("stages truncated: the stage cap was hit, the tree is incomplete\n")
	}
	for index, finding := range result.GetFindings() {
		if index == 0 {
			output.WriteString("findings:\n")
		}
		fmt.Fprintf(&output, "  %s %s", strings.ToLower(strings.TrimPrefix(finding.GetLevel().String(), "FINDING_")), finding.GetCode())
		if len(finding.GetStages()) > 0 {
			stages := make([]string, 0, len(finding.GetStages()))
			for _, id := range finding.GetStages() {
				stages = append(stages, fmt.Sprint(id))
			}
			output.WriteString(" stages=" + strings.Join(stages, ","))
		}
		if attrs := formatConnectionTestAttrs(finding.GetAttrs()); attrs != "" {
			output.WriteString(" " + attrs)
		}
		output.WriteString("\n")
	}
	return output.String()
}
