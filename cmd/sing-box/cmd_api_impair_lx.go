//go:build with_lx_command

package main

import (
	"bytes"
	stdjson "encoding/json"
	"io"
	"os"
	"strings"

	"github.com/sagernet/sing-box/daemon"

	"github.com/spf13/cobra"
)

// lx: SPEC 164 — runtime control of an impair test outbound: change the
// link now, replace the schedule, read the state. Each prints the status.

var commandAPIImpair = &cobra.Command{
	Use:   "impair",
	Short: "Control an impair test outbound at runtime",
}

var commandAPIImpairStatus = &cobra.Command{
	Use:   "status <tag>",
	Short: "Show the link, counters and schedule position of an impair outbound",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAPIImpair(func(client daemon.StartedServiceClient) (*daemon.ImpairStatus, error) {
			return client.GetImpairStatus(globalCtx, &daemon.ImpairStatusRequest{OutboundTag: args[0]})
		})
	},
}

var commandAPIImpairSet = &cobra.Command{
	Use:   "set <tag> <entry>",
	Short: `Change the line now: a schedule entry without "at", e.g. '{"link_params": {"outage": true}}'`,
	Long: `Change an impair outbound now, on top of its running schedule.
<entry> is one schedule entry without "at" (JSON, or @file, or - for stdin):
  '{"link_params": {"outage": true, "reset": true}}'
  '{"link_params": {"profile": "lte-walk", "loss": "2%"}}'
  '{"link_params": {"down": {"delay": "300ms"}}}'
  '{"sublinks": [{"match": {"domain": ["site3.test"]}, "link_params": {"down": {"outage": true}}}]}'`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		entry, err := readImpairArgument(args[1])
		if err != nil {
			return err
		}
		return runAPIImpair(func(client daemon.StartedServiceClient) (*daemon.ImpairStatus, error) {
			return client.SetImpair(globalCtx, &daemon.SetImpairRequest{OutboundTag: args[0], Entry: entry})
		})
	},
}

var (
	commandAPIImpairScheduleStart  string
	commandAPIImpairScheduleRepeat string
)

var commandAPIImpairSchedule = &cobra.Command{
	Use:   "schedule <tag> <entries>",
	Short: "Replace the schedule: the line and sublinks go back to the config, then the entries run",
	Long: `Replace the schedule of an impair outbound. The line and the sublinks go back
to the config, then <entries> (a "schedule" list: JSON, or @file, or - for stdin)
run from now, or from the next traffic with --start first_use, and start over
every --repeat when it is set. [] stops the schedule and only restores the config.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		entries, err := readImpairArgument(args[1])
		if err != nil {
			return err
		}
		return runAPIImpair(func(client daemon.StartedServiceClient) (*daemon.ImpairStatus, error) {
			return client.ScheduleImpair(globalCtx, &daemon.ScheduleImpairRequest{OutboundTag: args[0], Entries: entries, Start: commandAPIImpairScheduleStart, Repeat: commandAPIImpairScheduleRepeat})
		})
	},
}

func init() {
	commandAPIImpairSchedule.Flags().StringVar(&commandAPIImpairScheduleStart, "start", "now", `when the entries count from: "now" or "first_use" (the next traffic)`)
	commandAPIImpairSchedule.Flags().StringVar(&commandAPIImpairScheduleRepeat, "repeat", "", `the length of one round ("60s"): the schedule starts over every round`)
	commandAPIImpair.AddCommand(commandAPIImpairStatus, commandAPIImpairSet, commandAPIImpairSchedule)
	commandAPIRoot.AddCommand(commandAPIImpair)
}

// readImpairArgument takes JSON inline, from @file, or from stdin for "-".
func readImpairArgument(argument string) (string, error) {
	switch {
	case argument == "-":
		content, err := io.ReadAll(os.Stdin)
		return string(content), err
	case strings.HasPrefix(argument, "@"):
		content, err := os.ReadFile(argument[1:])
		return string(content), err
	}
	return argument, nil
}

func runAPIImpair(call func(client daemon.StartedServiceClient) (*daemon.ImpairStatus, error)) error {
	clientConn, client, err := createAPIClient()
	if err != nil {
		return err
	}
	defer clientConn.Close()
	response, err := call(client)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if stdjson.Indent(&out, []byte(response.GetJson()), "", "  ") != nil {
		out.Reset()
		out.WriteString(response.GetJson())
	}
	out.WriteByte('\n')
	_, err = os.Stdout.Write(out.Bytes())
	return err
}
