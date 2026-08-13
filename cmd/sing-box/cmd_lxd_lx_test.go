//go:build with_lxd

package main

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/lxd"
)

// saveLxdGlobals snapshots the lxd command globals and the cobra Changed
// markers the code under test reads, restoring everything on cleanup so the
// tests stay order-independent.
func saveLxdGlobals(t *testing.T) {
	t.Helper()
	// Merge persistent flags into Flags() the way a real cobra execute would.
	_ = commandLxd.ParseFlags(nil)
	savedStateDir := lxdStateDir
	savedConfigForce := lxdConfigForce
	savedRun := lxdRun
	savedService := lxdService
	savedConfigPaths := configPaths
	stateDirFlag := commandLxd.Flags().Lookup("state-dir")
	savedStateDirChanged := stateDirFlag.Changed
	configFlag := mainCommand.PersistentFlags().Lookup("config")
	savedConfigChanged := configFlag.Changed
	t.Cleanup(func() {
		lxdStateDir = savedStateDir
		lxdConfigForce = savedConfigForce
		lxdRun = savedRun
		lxdService = savedService
		configPaths = savedConfigPaths
		stateDirFlag.Changed = savedStateDirChanged
		configFlag.Changed = savedConfigChanged
	})
}

// argValue returns the token following the given flag in an args slice.
func argValue(args []string, flag string) (string, bool) {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func TestDaemonArgsForServiceMinimalUnit(t *testing.T) {
	saveLxdGlobals(t)
	lxdConfigForce = filepath.Join("rel", "force.json")
	lxdService = "install"
	lxdRun = true
	configPaths = []string{filepath.Join("rel", "seed.json")}
	mainCommand.PersistentFlags().Lookup("config").Changed = true

	args := daemonArgsForService(commandLxd, "rel-state")

	if len(args) == 0 || args[0] != "lxd" {
		t.Fatal("service args must start with the lxd subcommand, got:", args)
	}
	// The unit carries ONLY the state dir and boot parameters — connection
	// settings live in daemon.json, and connection flags no longer exist.
	for _, banned := range []string{"--service", "--secret", "--secret-file", "--listen", "--tls"} {
		if slices.Contains(args, banned) {
			t.Fatalf("%s must not ride into the unit: %v", banned, args)
		}
	}
	if !slices.Contains(args, "--run") {
		t.Fatal("--run must be preserved:", args)
	}
	// A launchd/systemd unit starts with cwd "/", so every path must come out
	// absolute — resolved against the cwd the operator installed from.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for flag, relative := range map[string]string{
		"--state-dir":    "rel-state",
		"--config-force": filepath.Join("rel", "force.json"),
		"-c":             filepath.Join("rel", "seed.json"),
	} {
		value, found := argValue(args, flag)
		if !found {
			t.Fatalf("%s missing from service args: %v", flag, args)
		}
		if !filepath.IsAbs(value) {
			t.Fatalf("%s must be absolute, got %q", flag, value)
		}
		if want := filepath.Join(cwd, relative); value != want {
			t.Fatalf("%s resolved to %q, want %q", flag, value, want)
		}
	}
}

func TestServiceStateDirDefault(t *testing.T) {
	saveLxdGlobals(t)
	// The flag default "lxd-state" is cwd-relative and useless in a service:
	// an unchanged flag must be replaced with the platform support directory.
	lxdStateDir = "lxd-state"
	commandLxd.Flags().Lookup("state-dir").Changed = false

	for _, userScope := range []bool{false, true} {
		got := serviceStateDir(commandLxd, userScope)
		if got != lxd.DefaultServiceStateDir(userScope) {
			t.Fatalf("userScope=%v: default state-dir is %q, want %q", userScope, got, lxd.DefaultServiceStateDir(userScope))
		}
	}

	// An explicit flag wins and is made absolute.
	commandLxd.Flags().Lookup("state-dir").Changed = true
	lxdStateDir = "rel-state"
	got := serviceStateDir(commandLxd, false)
	if !filepath.IsAbs(got) || filepath.Base(got) != "rel-state" {
		t.Fatalf("explicit state-dir must come out absolute, got %q", got)
	}
}

// TestPrepareServiceConfigDecidesEverything pins the flagless install: it
// picks a free loopback port itself (19091+), forces mTLS, generates the
// secret — and a reinstall keeps both the address (enrolled clients pin it)
// and the secret.
func TestPrepareServiceConfigDecidesEverything(t *testing.T) {
	saveLxdGlobals(t)
	dir := t.TempDir()

	// userScope=true skips the root requirement (a user agent installs as the
	// invoking user) — the config logic is scope-independent.
	first, err := prepareServiceConfig(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	firstListen := first.Listen.Advertise()
	if !strings.HasPrefix(firstListen, "127.0.0.1:") {
		t.Fatalf("install must pick a loopback address, got %q", firstListen)
	}
	port, err := strconv.Atoi(strings.TrimPrefix(firstListen, "127.0.0.1:"))
	if err != nil || port < serviceScanPortStart || port >= serviceScanPortStart+serviceScanPortTries {
		t.Fatalf("port must come from the %d+ scan range, got %q", serviceScanPortStart, firstListen)
	}
	if !first.TLS {
		t.Fatal("a service must always be mTLS")
	}
	if len(first.Secret) != 64 {
		t.Fatalf("a missing secret must be generated (64 hex chars), got %q", first.Secret)
	}

	// Reinstall: address and secret must survive — moving the channel or
	// rotating the secret silently would strand enrolled clients/operators.
	second, err := prepareServiceConfig(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if second.Listen.Advertise() != first.Listen.Advertise() || second.Secret != first.Secret {
		t.Fatalf("reinstall must keep address and secret: %+v vs %+v", second, first)
	}
}

func TestFirstFreeLoopbackAddrSkipsTaken(t *testing.T) {
	// Find a genuinely free port, occupy it, and check the scan steps over it.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	holder, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(base))
	if err != nil {
		t.Skip("could not re-bind probe port:", err)
	}
	defer holder.Close()

	got, err := firstFreeLoopbackAddr(base, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got == "127.0.0.1:"+strconv.Itoa(base) {
		t.Fatal("scan returned the taken port")
	}
}

// TestDaemonConnectionFileOrDevDefaults: the daemon has exactly two states —
// daemon.json rules, or fixed dev defaults. No flags, no merge.
func TestDaemonConnectionFileOrDevDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := lxd.SaveDaemonConfig(dir, lxd.DaemonConfig{
		Listen: lxd.ListenAddress("127.0.0.1:28080"), TLS: true, Secret: "from-file",
	}); err != nil {
		t.Fatal(err)
	}
	config, installed, err := daemonConnection(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !installed || config.Listen.Advertise() != "127.0.0.1:28080" || !config.TLS || config.Secret != "from-file" {
		t.Fatalf("daemon.json must own the settings, got %v %+v", installed, config)
	}

	config, installed, err = daemonConnection(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if installed || config.Listen.Advertise() != devDefaultListen || config.TLS || config.Secret != "" {
		t.Fatalf("no file must mean plain dev defaults, got %v %+v", installed, config)
	}
}

// TestClientConnectionRequiresFile: client subcommands read the daemon's
// daemon.json; without one there is no daemon to talk to.
func TestClientConnectionRequiresFile(t *testing.T) {
	dir := t.TempDir()
	if err := lxd.SaveDaemonConfig(dir, lxd.DaemonConfig{
		Listen: lxd.ListenAddress("127.0.0.1:28080"), TLS: true, Secret: "s",
	}); err != nil {
		t.Fatal(err)
	}
	listen, useTLS, secret, err := clientConnection(dir)
	if err != nil {
		t.Fatal(err)
	}
	if listen != "127.0.0.1:28080" || !useTLS || secret != "s" {
		t.Fatalf("client must read the file, got %q %v %q", listen, useTLS, secret)
	}

	if _, _, _, err = clientConnection(t.TempDir()); err == nil {
		t.Fatal("client commands without daemon.json must fail loudly")
	}
}
