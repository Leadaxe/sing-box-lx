//go:build with_lxd

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	lxdKeepCopy    bool
	lxdDryRun      bool
	lxdExecDir     string
	lxdAllowUnsafe bool
	lxdClientName  string
	// SPEC 103 §2.13: the pairing invite into a file instead of stdout.
	lxdInviteOut       string
	lxdInviteName      string
	lxdClientInviteOut string
)

// defaultInviteName labels the client an --invite-out install pairs: the
// launcher's own name (SPEC 103 §2.13).
const defaultInviteName = "singbox-launcher"

// pinCronetLibrary, when a build sets it (cronetpin_windows_lx.go), keeps a
// privileged core from loading libcronet.dll out of PATH (SPEC 103 §4.2 p. 2).
var pinCronetLibrary func()

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
	serviceUsage := "install (system LaunchDaemon, root) | install-user (per-user LaunchAgent, no sudo) | copy (root-owned copy only, no service; root) | uninstall | status (exit 0 OK, 2 reinstall needed, 3 not installed, 4 copy only, 5 not running)"
	allowUnsafeUsage := "debug only: let the root launchd service start from a binary that is not a root-owned copy (logs a WARN instead of refusing)"
	execDirUsage := "with --service=install|copy|uninstall|status: directory for the root-owned binary copy sing-box-lxd and its sidecar (default /Library/PrivilegedHelperTools, which must exist; a directory given here is created if missing); every component from / must be root-owned and not group/world-writable"
	if runtime.GOOS == "windows" {
		serviceUsage = "install (Windows service sing-box-lxd; elevated) | copy (protected copy only, no service; elevated) | uninstall (elevated) | status (no elevation; exit 0 OK, 2 reinstall needed, 3 not installed, 4 copy only, 5 not running)"
		allowUnsafeUsage = "debug only: let the Windows service start from a binary that is not a protected copy (logs a WARN instead of refusing)"
		execDirUsage = `with --service=install|copy|uninstall|status: directory for the protected copy sing-box-lxd.exe (with libcronet.dll when it lies beside this binary) and its sidecar (default <ProgramFiles>\sing-box-lxd, created if missing); every component from the volume root must have an administrative owner and not be replaceable by other accounts`
	}
	commandLxd.Flags().StringVar(&lxdService, "service", "", serviceUsage)
	commandLxd.Flags().BoolVar(&lxdPurge, "purge", false, "with --service=uninstall: also delete the state directory (clients, last-good, keys)")
	commandLxd.Flags().BoolVar(&lxdKeepCopy, "keep-copy", false, "with --service=uninstall: remove the service but keep the root-owned copy and its sidecar for non-service use (--purge still only concerns the state)")
	commandLxd.Flags().BoolVar(&lxdDryRun, "dry-run", false, "with --service: show what would be done, change nothing")
	commandLxd.Flags().BoolVar(&lxdAllowUnsafe, "allow-unsafe-exec", false, allowUnsafeUsage)
	commandLxd.Flags().StringVar(&lxdExecDir, "exec-dir", "", execDirUsage)
	commandLxd.Flags().StringVar(&lxdInviteOut, "invite-out", "", "with --service=install: write the pairing invite into this new file (created exclusively, 0600) instead of printing it; a failed mint exits 1")
	commandLxd.Flags().StringVar(&lxdInviteName, "invite-name", "", "with --service=install: the name the paired client gets (default "+defaultInviteName+" with --invite-out, none without)")

	commandLxd.AddCommand(commandLxdClient)
	commandLxdClientAdd.Flags().StringVar(&lxdClientName, "name", "", "human label for the client (1-64 printable characters; a named invite replaces the client of that name)")
	commandLxdClientAdd.Flags().StringVar(&lxdClientInviteOut, "invite-out", "", "write the invite into this new file (created exclusively, 0600) instead of printing it")
	commandLxdClient.AddCommand(commandLxdClientAdd)
	commandLxdClient.AddCommand(commandLxdClientList)
	commandLxdClient.AddCommand(commandLxdClientRemove)

	mainCommand.AddCommand(commandLxd)

	// Root running the core outside the lxd service — the launcher's classic
	// TUN mode elevates `sing-box run` — gets the same executable check,
	// which outside the launchd job of this label only warns (SPEC 100).
	// Wraps Run only while upstream defines the command through Run; if it
	// moves to RunE this hook drops out instead of calling nil, and the
	// guard test in cmd_lxd_lx_test.go flags the move.
	if upstreamRun := commandRun.Run; upstreamRun != nil {
		commandRun.Run = func(cmd *cobra.Command, args []string) {
			if err := lxd.CheckServiceExecutable(false, false); err != nil {
				log.Fatal(err)
			}
			if pinCronetLibrary != nil && lxd.ServiceActionPrivileged() {
				pinCronetLibrary()
			}
			upstreamRun(cmd, args)
		}
	}
}

func lxdMain(cmd *cobra.Command) error {
	// The service branch comes first — it manages launchd, not the daemon.
	if lxdService != "" {
		return runServiceAction(cmd)
	}
	if cmd.Flags().Changed("exec-dir") {
		return E.New("--exec-dir needs --service=install, copy, uninstall or status")
	}
	if lxdKeepCopy {
		return E.New("--keep-copy needs --service=uninstall")
	}
	if cmd.Flags().Changed("invite-out") || cmd.Flags().Changed("invite-name") {
		return E.New("--invite-out and --invite-name need --service=install")
	}
	// Under the Windows SCM the handler owns the process (SPEC 103 §2.10):
	// daemon.json, then the log, then the self-check, so a refusal lands in
	// lxd.log.
	if lxd.IsWindowsService() {
		return lxd.RunService(func() (lxd.ServiceBody, error) {
			return lxdServiceBody(cmd)
		})
	}
	// A root launchd service must execute a root-owned copy (SPEC 100):
	// refused here, before anything binds or boots, with the path, owner
	// and mode in the message that launchd writes to lxd.log.
	if err := lxd.CheckServiceExecutable(lxdAllowUnsafe, true); err != nil {
		return err
	}
	daemonOptions, err := lxdDaemonOptions(cmd)
	if err != nil {
		return err
	}
	return lxd.Run(globalCtx, daemonOptions)
}

// lxdServiceBody prepares the daemon inside the SCM handler: daemon.json,
// the log taken over, the self-check (strict here), libcronet pinned, the
// working directory moved into the state dir — relative paths of the config
// (cache.db, a tailscale state_directory) resolve there, not in System32
// (SPEC 103 §4.2 p. 1).
func lxdServiceBody(cmd *cobra.Command) (lxd.ServiceBody, error) {
	daemonOptions, err := lxdDaemonOptions(cmd)
	if err != nil {
		return nil, err
	}
	daemonOptions.StateDir = absPathOr(daemonOptions.StateDir)
	release := lxd.TakeOverLog(daemonOptions)
	daemonOptions.LogTakenOver = true
	fail := func(err error) (lxd.ServiceBody, error) {
		if release != nil {
			release()
		}
		return nil, err
	}
	if err = lxd.CheckServiceExecutable(lxdAllowUnsafe, true); err != nil {
		return fail(err)
	}
	if pinCronetLibrary != nil {
		pinCronetLibrary()
	}
	if err = os.Chdir(daemonOptions.StateDir); err != nil {
		return fail(E.Cause(err, "lxd: enter the state directory"))
	}
	return func(ctx context.Context) error {
		if release != nil {
			defer release()
		}
		return lxd.Run(ctx, daemonOptions)
	}, nil
}

// lxdDaemonOptions reads the daemon's settings: daemon.json (or the dev
// defaults), the boot flags, and for an installed daemon its log.
func lxdDaemonOptions(cmd *cobra.Command) (lxd.Options, error) {
	fileConfig, installed, err := daemonConnection(lxdStateDir)
	if err != nil {
		return lxd.Options{}, err
	}

	// Config directories are not supported at all (FEATURE 014 §2) — reject
	// them loudly instead of silently ignoring a -C the operator relied on.
	if len(configDirectories) > 0 {
		return lxd.Options{}, E.New("lxd does not support -C config directories; pass a single -c file")
	}
	// -c is an optional seed: preRun injects a default config.json, so only an
	// explicitly passed flag counts.
	seed := ""
	if cmd.Root().PersistentFlags().Changed("config") {
		if len(configPaths) != 1 {
			return lxd.Options{}, E.New("lxd takes at most one -c config file")
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
		// Unlike the log settings below, this is not gated on `installed`: the
		// client directory serves a dev run too, and an absent list just means
		// the platform defaults.
		DHCPLeaseFiles: fileConfig.DHCPLeaseFiles,
	}
	// The rotated log file exists only for an installed daemon (daemon.json
	// present): a fileless dev run keeps its terminal, and Run additionally
	// skips the redirect when stdout is a TTY even here.
	if installed {
		daemonOptions.LogFile = lxd.DefaultLogPath(lxdStateDir)
		// daemon.json's log_file overrides the derived path (e.g. tmpfs on an
		// OpenWrt router). Absolute because a service unit runs with cwd "/".
		if fileConfig.LogFile != "" {
			daemonOptions.LogFile = absPathOr(fileConfig.LogFile)
		}
		daemonOptions.LogMaxSizeMB = fileConfig.LogMaxSizeMB
		daemonOptions.LogMaxBackups = fileConfig.LogMaxBackups
		daemonOptions.LogMaxAgeHours = fileConfig.LogMaxAgeHours
	}
	return daemonOptions, nil
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
	if config.Listen.IsZero() {
		config.Listen = lxd.ListenAddress(devDefaultListen)
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
	// A client dials ONE address; with several configured the first is the
	// operator's own preference (ListenConfig.Advertise).
	listen = fileConfig.Listen.Advertise()
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
	// --exec-dir places the root-owned copy of the binary (SPEC 100);
	// absolute, because the plist and the invariant need it so.
	execDir := ""
	if cmd.Flags().Changed("exec-dir") {
		switch lxdService {
		case "install", "copy", "uninstall", "status":
		default:
			return E.New("--exec-dir applies to --service=install, copy, uninstall and status (the root-owned copy) only")
		}
		execDir = absPathOr(lxdExecDir)
	}
	if lxdKeepCopy && lxdService != "uninstall" {
		return E.New("--keep-copy applies to --service=uninstall only")
	}
	invite, err := installInviteOptions(cmd)
	if err != nil {
		return err
	}
	switch lxdService {
	case "install", "install-user":
		userScope := lxdService == "install-user"
		if userScope {
			if err = lxd.CheckUserScope(); err != nil {
				return err
			}
		}
		// The invite file must not exist: refused before anything changes
		// (SPEC 103 §2.13).
		if invite.path != "" {
			if lxd.ServiceInstallIsAdvisory {
				return E.New("--invite-out needs a platform where --service=install installs the service (macOS, Windows)")
			}
			if err = lxd.CheckInviteFileAbsent(invite.path); err != nil {
				return err
			}
		}
		stateDir := serviceStateDir(cmd, userScope)
		// Two ways to end up printing instead of installing: the platform's
		// installer is advisory (linux), or the operator asked for a dry run.
		// Neither may prepare daemon.json or mint an invite — nothing was
		// installed, so there is no daemon to pair with.
		if lxd.ServiceInstallIsAdvisory || lxdDryRun {
			if userScope {
				return lxd.InstallUserService(daemonArgsForService(cmd, stateDir), lxdDryRun)
			}
			if err = lxd.InstallService(daemonArgsForService(cmd, stateDir), execDir, lxdDryRun); err != nil {
				return err
			}
			if invite.path != "" {
				fmt.Println("lxd: would write the invite for client", strconvQuote(invite.name), "to", invite.path)
			}
			return nil
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
			err = lxd.InstallService(daemonArgsForService(cmd, stateDir), execDir, false)
		}
		if err != nil {
			return err
		}
		return printServiceSummary(config, stateDir, userScope, invite)
	case "copy":
		// Only the root-owned copy and its sidecar: no plist, no launchd —
		// for a launcher that runs the core as root by itself.
		return lxd.InstallServiceCopy(execDir, lxdDryRun)
	case "uninstall":
		return lxd.UninstallService(lxdPurge, lxdKeepCopy, execDir, lxdDryRun)
	case "status":
		// Read-only, no root needed. The exit code is the launcher's
		// contract: 0 OK, 2 MISMATCH/UNSAFE, 3 NOT INSTALLED, 4 COPY ONLY,
		// 5 NOT RUNNING, 1 an error.
		verdict, err := lxd.ServiceStatus(execDir)
		if err != nil {
			return err
		}
		if code := verdict.ExitCode(); code != 0 {
			os.Exit(code)
		}
		return nil
	default:
		return E.New("--service must be install, install-user, copy, uninstall, or status")
	}
}

// inviteDelivery is where install's pairing invite goes: a new file
// (--invite-out) or stdout, and the client name minted with it.
type inviteDelivery struct {
	path string
	name string
}

// installInviteOptions validates --invite-out/--invite-name (SPEC 103
// §2.13): install only; the name follows the mint norm; with a file the name
// defaults to the launcher's, without one it is --invite-name or none, as
// before.
func installInviteOptions(cmd *cobra.Command) (inviteDelivery, error) {
	outSet, nameSet := cmd.Flags().Changed("invite-out"), cmd.Flags().Changed("invite-name")
	if !outSet && !nameSet {
		return inviteDelivery{}, nil
	}
	if lxdService != "install" && lxdService != "install-user" {
		return inviteDelivery{}, E.New("--invite-out and --invite-name apply to --service=install only")
	}
	if outSet && lxdInviteOut == "" {
		return inviteDelivery{}, E.New("--invite-out needs a file path")
	}
	name, err := lxd.NormalizeClientName(installInviteName(lxdInviteOut, lxdInviteName, nameSet))
	if err != nil {
		return inviteDelivery{}, err
	}
	return inviteDelivery{path: lxdInviteOut, name: name}, nil
}

// installInviteName: --invite-name wins; with --invite-out and no name the
// client is the launcher; otherwise no name, as before.
func installInviteName(inviteOut, inviteName string, nameSet bool) string {
	switch {
	case nameSet:
		return inviteName
	case inviteOut != "":
		return defaultInviteName
	default:
		return ""
	}
}

func strconvQuote(value string) string {
	return fmt.Sprintf("%q", value)
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
	// Same privilege requirement as the service write below, checked first so
	// the operator gets the actionable message before any disk mutation.
	if !userScope && !lxd.ServiceActionPrivileged() {
		return lxd.DaemonConfig{}, lxd.ServiceInstallPrivilegeError()
	}
	// Windows: the data dir is taken over BEFORE the secret is written into
	// it (SPEC 103 §2.3); elsewhere a no-op.
	if !userScope {
		if err := lxd.PrepareServiceHome(os.Stdout); err != nil {
			return lxd.DaemonConfig{}, err
		}
	}
	config, _, err := lxd.LoadDaemonConfig(stateDir)
	if err != nil {
		return lxd.DaemonConfig{}, err
	}
	if config.Listen.IsZero() {
		address, addressErr := firstFreeLoopbackAddr(serviceScanPortStart, serviceScanPortTries)
		if addressErr != nil {
			return lxd.DaemonConfig{}, addressErr
		}
		config.Listen = lxd.ListenAddress(address)
	}
	config.TLS = true
	// Windows keeps the log in logs\ beside the launcher's classic.log
	// (SPEC 103 §2.12); an operator's log_file stays.
	if defaultLog := lxd.ServiceDefaultLogFile(); !userScope && config.LogFile == "" && defaultLog != "" {
		config.LogFile = defaultLog
	}
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
// invite code burns on the first enroll. With --invite-out the invite goes
// into the file instead, and a failed mint is an error: the launcher judges
// by the exit code (SPEC 103 §2.13); without it a failed mint only warns, as
// before.
func printServiceSummary(config lxd.DaemonConfig, stateDir string, userScope bool, invite inviteDelivery) error {
	fmt.Println("lxd: control channel:", strings.Join(config.Listen.Addresses(), ", "))
	fmt.Println("lxd: admin secret:   ", config.Secret)
	fmt.Println("lxd: settings file:  ", filepath.Join(stateDir, "daemon.json"))
	fmt.Println("lxd: restart command:", lxd.ServiceRestartCommand(userScope))

	var file *os.File
	if invite.path != "" {
		var err error
		if file, err = lxd.CreateInviteFile(invite.path); err != nil {
			return E.Cause(err, "service installed, but the invite file cannot be created")
		}
	}
	client := lxd.NewLocalClient(config.Listen.Advertise(), config.Secret, config.TLS)
	deadline := time.Now().Add(15 * time.Second)
	for {
		code, err := client.MintClientCode(invite.name)
		if err == nil {
			return deliverInvite(code, file)
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "lxd: service installed, but minting an invite failed:", err)
			fmt.Fprintln(os.Stderr, "lxd: mint one manually: "+lxd.PrivilegedCommand(os.Args[0]+" lxd client add"))
			if file != nil {
				lxd.DiscardInviteFile(file)
				return E.Cause(err, "service installed, but no invite was written to ", invite.path)
			}
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// deliverInvite writes the invite into the file made for it, or prints it.
func deliverInvite(invite string, file *os.File) error {
	if file == nil {
		fmt.Println("lxd: pair a launcher with this one-time invite:")
		fmt.Println(invite)
		return nil
	}
	path := file.Name()
	if err := lxd.WriteInvite(file, invite); err != nil {
		_ = os.Remove(path)
		return err
	}
	fmt.Println("lxd: invite written to", path)
	return nil
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
func clientStateDir(cmd *cobra.Command) (string, error) {
	if cmd.Flags().Changed("state-dir") {
		return lxdStateDir, nil
	}
	if dir, found := lxd.FindServiceStateDir(); found {
		return dir, nil
	}
	// Windows: the service's daemon.json exists but is closed to this
	// token — say so instead of falling back to the dev state dir.
	if err := lxd.ServiceStateDirAccessError(); err != nil {
		return "", err
	}
	return lxdStateDir, nil
}

func lxdClientCommandClient(cmd *cobra.Command) (*lxd.LocalClient, error) {
	stateDir, err := clientStateDir(cmd)
	if err != nil {
		return nil, err
	}
	listen, useTLS, secret, err := clientConnection(stateDir)
	if err != nil {
		return nil, err
	}
	return lxd.NewLocalClient(listen, secret, useTLS), nil
}

func lxdClientAdd(cmd *cobra.Command) error {
	name, err := lxd.NormalizeClientName(lxdClientName)
	if err != nil {
		return err
	}
	client, err := lxdClientCommandClient(cmd)
	if err != nil {
		return err
	}
	// The file is made before the mint: an existing one refuses, and a mint
	// without a place to put the invite would only burn the previous code.
	var file *os.File
	if lxdClientInviteOut != "" {
		if file, err = lxd.CreateInviteFile(lxdClientInviteOut); err != nil {
			return err
		}
	}
	invite, err := client.MintClientCode(name)
	if err != nil {
		if file != nil {
			lxd.DiscardInviteFile(file)
		}
		return err
	}
	if file != nil {
		return deliverInvite(invite, file)
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
