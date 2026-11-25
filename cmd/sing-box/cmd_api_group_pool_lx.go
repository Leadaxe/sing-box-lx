//go:build with_lx_command

package main

import (
	"strconv"

	"github.com/sagernet/sing-box/daemon"
	F "github.com/sagernet/sing/common/format"

	"github.com/spf13/cobra"
)

// lx: SPEC 019 v2 / SPEC 138 — the round_robin rotation pool of a urltest group with
// the self-heal state, from GetPool.

var commandAPIGroupPool = &cobra.Command{
	Use:   "pool <group>",
	Short: "Show the round_robin rotation pool of a urltest group",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAPIGroupPool(args[0])
	},
}

func init() {
	commandAPIGroup.AddCommand(commandAPIGroupPool)
}

func runAPIGroupPool(groupTag string) error {
	clientConn, client, err := createAPIClient()
	if err != nil {
		return err
	}
	defer clientConn.Close()
	pool, err := client.GetPool(globalCtx, &daemon.GetPoolRequest{GroupTag: groupTag})
	if err != nil {
		return err
	}
	if len(pool.GetSlots()) == 0 {
		writeStderrLine("no pool: unknown tag or not a round_robin group")
		return nil
	}
	block := blockWriter{}
	block.addLine("Shifted keys", strconv.FormatUint(uint64(pool.GetShiftedKeys()), 10))
	block.addLine("Address problems", strconv.FormatUint(uint64(pool.GetMarkedKeys()), 10))
	block.addLine("Frozen", formatPoolFrozen(pool))
	block.flush()
	table := tableWriter{
		header: []string{"SLOT", "TAG", "DELAY", "STATE", "CONNECTIONS", "FAILURES"},
	}
	for _, slot := range pool.GetSlots() {
		state := "live"
		switch {
		case slot.GetTag() == "":
			state = "empty" // the node was struck out and no replacement answered yet
		case slot.GetQuarantined():
			state = "quarantined"
		case slot.GetDelay() == 0:
			state = "dead"
		}
		table.addRow(strconv.FormatUint(uint64(slot.GetSlot()), 10), slot.GetTag(), formatDelay(int32(slot.GetDelay())), state,
			strconv.FormatUint(uint64(slot.GetConnections()), 10), strconv.FormatUint(uint64(slot.GetConfirmedFailures()), 10))
	}
	table.flush()
	return nil
}

func formatPoolFrozen(pool *daemon.PoolList) string {
	if !pool.GetFrozen() {
		return "no"
	}
	return F.ToString("yes, ", pool.GetFrozenSeconds(), "s left")
}
