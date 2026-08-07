//go:build with_lx_command

package main

import (
	"fmt"
	"os"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/lxd"
	E "github.com/sagernet/sing/common/exceptions"

	"github.com/spf13/cobra"
)

var (
	lxdListen      string
	lxdSecret      string
	lxdSecretFile  string
	lxdStateDir    string
	lxdConfigForce string
	lxdRun         bool
	lxdTLS         bool
	lxdService     string
	lxdClientName  string
)

// commandLxd is the daemon: bare `sing-box lxd` hosts the core in-process
// behind a reload-surviving control channel. Client management lives under
// `sing-box lxd client …`.
var commandLxd = &cobra.Command{
	Use:   "lxd",
	Short: "Run the sing-box-lx daemon: host the core in-process behind a reload-surviving control channel",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if err := lxdMain(cmd); err != nil {
			log.Fatal(err)
		}
	},
}

var commandLxdClient = &cobra.Command{
	Use:   "client",
	Short: "Manage trusted launcher clients (mTLS enrollment)",
}

var commandLxdClientAdd = &cobra.Command{
	Use:   "add",
	Short: "Mint a one-time enrollment invite for a new client",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if err := lxdClientAdd(); err != nil {
			log.Fatal(err)
		}
	},
}

var commandLxdClientList = &cobra.Command{
	Use:   "list",
	Short: "List trusted clients",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if err := lxdClientList(); err != nil {
			log.Fatal(err)
		}
	},
}

var commandLxdClientRemove = &cobra.Command{
	Use:   "remove <name-or-fingerprint>",
	Short: "Revoke a trusted client",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if err := lxdClientRemove(args[0]); err != nil {
			log.Fatal(err)
		}
	},
}

func init() {
	// Connection flags are persistent so the `client` subcommands share them
	// with the daemon command.
	commandLxd.PersistentFlags().StringVar(&lxdListen, "listen", "127.0.0.1:9091", "control channel listen address")
	commandLxd.PersistentFlags().StringVar(&lxdSecret, "secret", "", "control channel Bearer secret (avoid on shared hosts — visible in ps)")
	commandLxd.PersistentFlags().StringVar(&lxdSecretFile, "secret-file", "", "read the Bearer secret from a 0600 file (preferred over --secret)")
	commandLxd.PersistentFlags().BoolVar(&lxdTLS, "tls", false, "enable the mTLS control plane with client enrollment")
	commandLxd.Flags().StringVar(&lxdStateDir, "state-dir", "lxd-state", "directory for last-good config, run-state, and client trust")
	commandLxd.Flags().StringVar(&lxdConfigForce, "config-force", "", "always boot from this config file, overriding recorded last-good")
	commandLxd.Flags().BoolVar(&lxdRun, "run", false, "force the core up regardless of recorded run-state")
	commandLxd.Flags().StringVar(&lxdService, "service", "", "install (system LaunchDaemon, root) | install-user (per-user LaunchAgent, no sudo) | uninstall | print")

	commandLxd.AddCommand(commandLxdClient)
	commandLxdClientAdd.Flags().StringVar(&lxdClientName, "name", "", "human label for the client")
	commandLxdClient.AddCommand(commandLxdClientAdd)
	commandLxdClient.AddCommand(commandLxdClientList)
	commandLxdClient.AddCommand(commandLxdClientRemove)

	mainCommand.AddCommand(commandLxd)
}

func resolveSecret() (string, error) {
	if lxdSecretFile != "" {
		content, err := os.ReadFile(lxdSecretFile)
		if err != nil {
			return "", E.Cause(err, "read secret file")
		}
		return trimSecret(string(content)), nil
	}
	return lxdSecret, nil
}

func trimSecret(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

func lxdMain(cmd *cobra.Command) error {
	secret, err := resolveSecret()
	if err != nil {
		return err
	}

	if lxdService != "" {
		return runServiceAction(cmd)
	}

	// -c is an optional seed: preRun injects a default config.json, so only an
	// explicitly passed flag counts.
	seed := ""
	if cmd.Root().PersistentFlags().Changed("config") {
		if len(configDirectories) > 0 || len(configPaths) != 1 {
			return E.New("lxd takes at most one -c config file")
		}
		seed = configPaths[0]
	}

	return lxd.Run(globalCtx, lxd.Options{
		ConfigPath:  seed,
		ConfigForce: lxdConfigForce,
		Run:         lxdRun,
		Listen:      lxdListen,
		Secret:      secret,
		TLS:         lxdTLS,
		StateDir:    lxdStateDir,
	})
}

// runServiceAction registers/unregisters the daemon with the OS service
// manager. On install, the current daemon flags (minus --service) become the
// service's command line, so "what you ran is what gets registered".
func runServiceAction(cmd *cobra.Command) error {
	switch lxdService {
	case "install":
		return lxd.InstallService(daemonArgsForService(cmd))
	case "install-user":
		return lxd.InstallUserService(daemonArgsForService(cmd))
	case "uninstall":
		return lxd.UninstallService()
	case "print":
		return lxd.PrintService(daemonArgsForService(cmd))
	default:
		return E.New("--service must be install, install-user, uninstall, or print")
	}
}

// daemonArgsForService reconstructs the daemon invocation without --service,
// so the installed unit runs the same command the operator tested.
func daemonArgsForService(cmd *cobra.Command) []string {
	args := []string{"lxd", "--listen", lxdListen}
	if lxdStateDir != "" {
		args = append(args, "--state-dir", lxdStateDir)
	}
	if lxdConfigForce != "" {
		args = append(args, "--config-force", lxdConfigForce)
	}
	if lxdRun {
		args = append(args, "--run")
	}
	if lxdTLS {
		args = append(args, "--tls")
	}
	if lxdSecretFile != "" {
		args = append(args, "--secret-file", lxdSecretFile)
	}
	if cmd.Root().PersistentFlags().Changed("config") && len(configPaths) == 1 {
		args = append(args, "-c", configPaths[0])
	}
	return args
}

func lxdClientCommandClient() (*lxd.LocalClient, error) {
	secret, err := resolveSecret()
	if err != nil {
		return nil, err
	}
	return lxd.NewLocalClient(lxdListen, secret, lxdTLS), nil
}

func lxdClientAdd() error {
	client, err := lxdClientCommandClient()
	if err != nil {
		return err
	}
	invite, err := client.MintClientCode(lxdClientName)
	if err != nil {
		return err
	}
	fmt.Println("copy this invite into the launcher:")
	fmt.Println(invite)
	return nil
}

func lxdClientList() error {
	client, err := lxdClientCommandClient()
	if err != nil {
		return err
	}
	out, err := client.ListClients()
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

func lxdClientRemove(target string) error {
	client, err := lxdClientCommandClient()
	if err != nil {
		return err
	}
	if err = client.RemoveClient(target); err != nil {
		return err
	}
	fmt.Println("removed", target)
	return nil
}
