//go:build with_lx_command

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/lxd"
	E "github.com/sagernet/sing/common/exceptions"

	"github.com/spf13/cobra"
)

// absPathOr resolves a path to absolute against the current working directory,
// falling back to the input on error. Service args must be absolute because a
// launchd/systemd unit runs with cwd "/".
func absPathOr(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

var (
	lxdStateDir    string
	lxdConfigForce string
	lxdRun         bool
	lxdService     string
	lxdPurge       bool
	lxdDryRun      bool
	lxdClientName  string
)

// devDefaultListen — адрес dev-запуска без daemon.json: plain h2c на
// loopback, без секрета (FEATURE 014 §2). Всё остальное — только через
// daemon.json: connection-флагов у демона НЕТ по построению, поэтому
// вопрос «кто побеждает — файл или флаг» не существует.
const devDefaultListen = "127.0.0.1:9091"

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
	Short: "Mint a one-time enrollment invite for a new client (needs sudo for a system service)",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if err := lxdClientAdd(cmd); err != nil {
			log.Fatal(err)
		}
	},
}

var commandLxdClientList = &cobra.Command{
	Use:   "list",
	Short: "List trusted clients",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if err := lxdClientList(cmd); err != nil {
			log.Fatal(err)
		}
	},
}

var commandLxdClientRemove = &cobra.Command{
	Use:   "remove <name-or-fingerprint>",
	Short: "Revoke a trusted client",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if err := lxdClientRemove(cmd, args[0]); err != nil {
			log.Fatal(err)
		}
	},
}

func init() {
	// Namely NO connection flags (--listen/--tls/--secret/…): the daemon's
	// connection settings live in <state-dir>/daemon.json exclusively.
	// A dev run without the file gets fixed defaults (plain h2c on
	// 127.0.0.1:9091, no secret); anything else — edit the file.
	commandLxd.PersistentFlags().StringVar(&lxdStateDir, "state-dir", "lxd-state", "daemon home: daemon.json, last-good config, run-state, client trust, keys")
	commandLxd.Flags().StringVar(&lxdConfigForce, "config-force", "", "always boot from this config file, overriding recorded last-good")
	commandLxd.Flags().BoolVar(&lxdRun, "run", false, "force the core up regardless of recorded run-state")
	commandLxd.Flags().StringVar(&lxdService, "service", "", "install (system LaunchDaemon, root) | install-user (per-user LaunchAgent, no sudo) | uninstall")
	commandLxd.Flags().BoolVar(&lxdPurge, "purge", false, "with --service=uninstall: also delete the state directory (clients, last-good, keys)")
	commandLxd.Flags().BoolVar(&lxdDryRun, "dry-run", false, "with --service: show what would be done, change nothing")

	commandLxd.AddCommand(commandLxdClient)
	commandLxdClientAdd.Flags().StringVar(&lxdClientName, "name", "", "human label for the client")
	commandLxdClient.AddCommand(commandLxdClientAdd)
	commandLxdClient.AddCommand(commandLxdClientList)
	commandLxdClient.AddCommand(commandLxdClientRemove)

	mainCommand.AddCommand(commandLxd)
}

func lxdMain(cmd *cobra.Command) error {
	// The service branch comes first — it manages launchd, not the daemon.
	if lxdService != "" {
		return runServiceAction(cmd)
	}

	fileConfig, installed, err := daemonConnection(lxdStateDir)
	if err != nil {
		return err
	}

	// Config directories are not supported at all (FEATURE 014 §2) — reject
	// them loudly instead of silently ignoring a -C the operator relied on.
	if len(configDirectories) > 0 {
		return E.New("lxd does not support -C config directories; pass a single -c file")
	}
	// -c is an optional seed: preRun injects a default config.json, so only an
	// explicitly passed flag counts.
	seed := ""
	if cmd.Root().PersistentFlags().Changed("config") {
		if len(configPaths) != 1 {
			return E.New("lxd takes at most one -c config file")
		}
		seed = configPaths[0]
	}

	daemonOptions := lxd.Options{
		ConfigPath:  seed,
		ConfigForce: lxdConfigForce,
		Run:         lxdRun,
		Listen:      fileConfig.Listen,
		Secret:      fileConfig.Secret,
		TLS:         fileConfig.TLS,
		StateDir:    lxdStateDir,
	}
	// The rotated log file exists only for an installed daemon (daemon.json
	// present): a fileless dev run keeps its terminal, and Run additionally
	// skips the redirect when stdout is a TTY even here.
	if installed {
		daemonOptions.LogFile = lxd.DefaultLogPath(lxdStateDir)
		daemonOptions.LogMaxSizeMB = fileConfig.LogMaxSizeMB
		daemonOptions.LogMaxBackups = fileConfig.LogMaxBackups
		daemonOptions.LogMaxAgeHours = fileConfig.LogMaxAgeHours
	}
	return lxd.Run(globalCtx, daemonOptions)
}

// daemonConnection resolves the DAEMON's settings with exactly one source of
// truth: daemon.json when present (installed=true), fixed dev defaults when
// absent (plain h2c on 127.0.0.1:9091, no secret). The file is never created
// implicitly — only --service=install or the operator's editor write it.
// Boot parameters (-c seed, --config-force, --run) are not connection
// settings and stay flag-only.
func daemonConnection(stateDir string) (config lxd.DaemonConfig, installed bool, err error) {
	config, installed, err = lxd.LoadDaemonConfig(stateDir)
	if err != nil {
		return lxd.DaemonConfig{}, false, err
	}
	if config.Listen == "" {
		config.Listen = devDefaultListen
	}
	return config, installed, nil
}

// clientConnection resolves the connection for the `client` subcommands.
// These are CLIENTS of a daemon and read its daemon.json; without one there
// is nothing to talk to (a fileless dev daemon runs plain WITHOUT the client
// registry, so `client …` is meaningless against it by construction).
func clientConnection(stateDir string) (listen string, useTLS bool, secret string, err error) {
	fileConfig, found, err := lxd.LoadDaemonConfig(stateDir)
	if err != nil {
		return "", false, "", err
	}
	if !found {
		return "", false, "", E.New("no daemon.json in ", stateDir, " — no installed daemon to talk to (pass --state-dir of the daemon's home)")
	}
	listen = fileConfig.Listen
	if listen == "" {
		listen = devDefaultListen
	}
	return listen, fileConfig.TLS, fileConfig.Secret, nil
}

// runServiceAction registers/unregisters the daemon with the OS service
// manager. On install, listen/tls/secret land in <state-dir>/daemon.json (the
// daemon's own settings file) and the unit's command line degenerates to
// `lxd --state-dir <dir>` — changing settings later is a file edit + restart,
// not a reinstall, and the secret never leaves the daemon's home.
//
// --dry-run makes any action show what it would do and change nothing. On
// linux every action is already advisory, so the flag is a no-op there by
// construction — the same output either way.
func runServiceAction(cmd *cobra.Command) error {
	switch lxdService {
	case "install", "install-user":
		userScope := lxdService == "install-user"
		stateDir := serviceStateDir(cmd, userScope)
		// Two ways to end up printing instead of installing: the platform's
		// installer is advisory (linux), or the operator asked for a dry run.
		// Neither may prepare daemon.json or mint an invite — nothing was
		// installed, so there is no daemon to pair with.
		if lxd.ServiceInstallIsAdvisory || lxdDryRun {
			if userScope {
				return lxd.InstallUserService(daemonArgsForService(cmd, stateDir), lxdDryRun)
			}
			return lxd.InstallService(daemonArgsForService(cmd, stateDir), lxdDryRun)
		}
		// Connection settings are owned by daemon.json, which install itself
		// materializes (free-port scan, generated secret). The connection
		// flags no longer exist on this command at all, so cobra rejects them
		// at parse time ("unknown flag") — nothing to re-check here.
		config, err := prepareServiceConfig(stateDir, userScope)
		if err != nil {
			return err
		}
		if userScope {
			err = lxd.InstallUserService(daemonArgsForService(cmd, stateDir), false)
		} else {
			err = lxd.InstallService(daemonArgsForService(cmd, stateDir), false)
		}
		if err != nil {
			return err
		}
		printServiceSummary(config, stateDir, userScope)
		return nil
	case "uninstall":
		return lxd.UninstallService(lxdPurge, lxdDryRun)
	default:
		return E.New("--service must be install, install-user, or uninstall")
	}
}

// serviceStateDir resolves the state dir a service should use: the explicit
// flag, else the platform's support directory (absolute — a launchd unit runs
// with cwd "/", so the cwd-relative dev default would resolve against the
// filesystem root and crash-loop).
func serviceStateDir(cmd *cobra.Command, userScope bool) string {
	if cmd.Flags().Changed("state-dir") {
		return absPathOr(lxdStateDir)
	}
	return lxd.DefaultServiceStateDir(userScope)
}

// serviceScanPortStart — первый порт, который install пробует для локального
// управляющего канала; занят → сканируем вверх. Диапазон 19091+ выбран
// подальше от Clash-конвенций (9090/9091) и типовых dev-портов.
const (
	serviceScanPortStart = 19091
	serviceScanPortTries = 100
)

// firstFreeLoopbackAddr returns 127.0.0.1:<port> for the first bindable port
// in [start, start+tries).
func firstFreeLoopbackAddr(start, tries int) (string, error) {
	for port := start; port < start+tries; port++ {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		listener, err := net.Listen("tcp", addr)
		if err == nil {
			_ = listener.Close()
			return addr, nil
		}
	}
	return "", E.New("no free loopback port in ", start, "-", start+tries-1)
}

// prepareServiceConfig materializes daemon.json for the service BEFORE the
// unit is bootstrapped (the daemon reads it on its very first start). No
// flags involved: install decides everything and prints it.
//
//   - listen: an existing daemon.json keeps its address (reinstalls must not
//     move the channel out from under enrolled clients); otherwise the first
//     free loopback port starting at 19091.
//   - tls: a service is ALWAYS mTLS — a plain service would be pointless
//     (launchers pair with certificates).
//   - secret: kept if present, generated otherwise — the daemon owns its
//     credential.
func prepareServiceConfig(stateDir string, userScope bool) (lxd.DaemonConfig, error) {
	// Same root requirement as the plist write below, checked first so the
	// operator gets the actionable message before any disk mutation.
	if !userScope && os.Getuid() != 0 {
		return lxd.DaemonConfig{}, E.New("--service=install needs root (run with sudo); for a per-user agent without sudo use --service=install-user")
	}
	config, _, err := lxd.LoadDaemonConfig(stateDir)
	if err != nil {
		return lxd.DaemonConfig{}, err
	}
	if config.Listen == "" {
		config.Listen, err = firstFreeLoopbackAddr(serviceScanPortStart, serviceScanPortTries)
		if err != nil {
			return lxd.DaemonConfig{}, err
		}
	}
	config.TLS = true
	if config.Secret == "" {
		config.Secret, err = lxd.GenerateSecret()
		if err != nil {
			return lxd.DaemonConfig{}, err
		}
	}
	if err = lxd.SaveDaemonConfig(stateDir, config); err != nil {
		return lxd.DaemonConfig{}, err
	}
	return config, nil
}

// printServiceSummary ends the install with everything the operator needs on
// one screen: the channel address, the admin secret, the settings file, the
// restart command, and a one-time pairing invite. The secret and the invite
// go to install's stdout — the operator's terminal, not the service log; the
// invite code burns on the first enroll.
func printServiceSummary(config lxd.DaemonConfig, stateDir string, userScope bool) {
	restartTarget := "system/" + "com.leadaxe.sing-box-lxd"
	restartPrefix := "sudo "
	if userScope {
		restartTarget = fmt.Sprintf("gui/%d/com.leadaxe.sing-box-lxd", os.Getuid())
		restartPrefix = ""
	}
	fmt.Println("lxd: control channel:", config.Listen)
	fmt.Println("lxd: admin secret:   ", config.Secret)
	fmt.Println("lxd: settings file:  ", filepath.Join(stateDir, "daemon.json"))
	fmt.Println("lxd: restart command:", restartPrefix+"launchctl kickstart -k "+restartTarget)

	client := lxd.NewLocalClient(config.Listen, config.Secret, config.TLS)
	deadline := time.Now().Add(15 * time.Second)
	for {
		invite, err := client.MintClientCode("")
		if err == nil {
			fmt.Println("lxd: pair a launcher with this one-time invite:")
			fmt.Println(invite)
			return
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "lxd: service installed, but minting an invite failed:", err)
			fmt.Fprintln(os.Stderr, "lxd: mint one manually: sudo "+os.Args[0]+" lxd client add")
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// daemonArgsForService reconstructs the daemon invocation without --service.
// Connection settings (listen/tls/secret) live in daemon.json, so the unit
// carries only the state dir plus the seed/run flags that are one-shot by
// nature. Every path is made absolute: a launchd service starts with cwd "/",
// so a relative --state-dir / --config-force / -c would resolve against the
// filesystem root and the daemon would crash-loop (observed on the first real
// system install).
func daemonArgsForService(cmd *cobra.Command, stateDir string) []string {
	args := []string{"lxd", "--state-dir", absPathOr(stateDir)}
	if lxdConfigForce != "" {
		args = append(args, "--config-force", absPathOr(lxdConfigForce))
	}
	if lxdRun {
		args = append(args, "--run")
	}
	if cmd.Root().PersistentFlags().Changed("config") && len(configPaths) == 1 {
		args = append(args, "-c", absPathOr(configPaths[0]))
	}
	return args
}

// clientStateDir picks the daemon home the `client` subcommands should talk
// to: the explicit --state-dir, else the installed service's home (system,
// then user — found by its daemon.json; reading the system one needs sudo),
// else the dev default. This is what lets bare `sudo sing-box lxd client add`
// work on a host with an installed service: listen/tls/secret all come from
// the daemon's own settings file.
func clientStateDir(cmd *cobra.Command) string {
	if cmd.Flags().Changed("state-dir") {
		return lxdStateDir
	}
	if dir, found := lxd.FindServiceStateDir(); found {
		return dir
	}
	return lxdStateDir
}

func lxdClientCommandClient(cmd *cobra.Command) (*lxd.LocalClient, error) {
	listen, useTLS, secret, err := clientConnection(clientStateDir(cmd))
	if err != nil {
		return nil, err
	}
	return lxd.NewLocalClient(listen, secret, useTLS), nil
}

func lxdClientAdd(cmd *cobra.Command) error {
	client, err := lxdClientCommandClient(cmd)
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

func lxdClientList(cmd *cobra.Command) error {
	client, err := lxdClientCommandClient(cmd)
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

func lxdClientRemove(cmd *cobra.Command, target string) error {
	client, err := lxdClientCommandClient(cmd)
	if err != nil {
		return err
	}
	if err = client.RemoveClient(target); err != nil {
		return err
	}
	fmt.Println("removed", target)
	return nil
}
