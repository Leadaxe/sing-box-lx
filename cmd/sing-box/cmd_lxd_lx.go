//go:build with_lx_command

package main

import (
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/lxd"
	E "github.com/sagernet/sing/common/exceptions"

	"github.com/spf13/cobra"
)

var (
	lxdListen string
	lxdSecret string
)

var commandLxd = &cobra.Command{
	Use:   "lxd",
	Short: "sing-box-lx daemon: host the core in-process behind a reload-surviving control channel",
}

var commandLxdRun = &cobra.Command{
	Use:   "run",
	Short: "Run the lxd daemon",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		err := lxdRun()
		if err != nil {
			log.Fatal(err)
		}
	},
}

func init() {
	commandLxdRun.Flags().StringVar(&lxdListen, "listen", "127.0.0.1:9091", "control channel listen address")
	commandLxdRun.Flags().StringVar(&lxdSecret, "secret", "", "control channel Bearer secret (empty disables authentication)")
	commandLxd.AddCommand(commandLxdRun)
	mainCommand.AddCommand(commandLxd)
}

func lxdRun() error {
	if len(configDirectories) > 0 || len(configPaths) != 1 {
		return E.New("lxd run takes exactly one -c config file")
	}
	return lxd.Run(globalCtx, lxd.Options{
		ConfigPath: configPaths[0],
		Listen:     lxdListen,
		Secret:     lxdSecret,
	})
}
