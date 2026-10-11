//go:build with_lxd && linux

package lxd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeInit swaps the host probe so the detector can be driven without the
// test machine's real init.
func fakeInit(t *testing.T, pid1Comm string, openwrt bool, systemdRun bool) {
	t.Helper()
	old := linuxInitProbe
	t.Cleanup(func() { linuxInitProbe = old })
	linuxInitProbe = func() linuxInitEnv {
		return linuxInitEnv{
			pid1Comm: pid1Comm + "\n",
			pathKind: func(path string) (bool, bool) {
				switch path {
				case "/etc/openwrt_release":
					return openwrt, false
				case "/run/systemd/system":
					return systemdRun, systemdRun
				}
				return false, false
			},
		}
	}
}

// capture runs fn with stdout redirected to a pipe and returns what it wrote.
func capture(t *testing.T, fn func() error) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	runErr := fn()
	os.Stdout = original
	_ = writer.Close()
	buf := make([]byte, 1<<16)
	n, _ := reader.Read(buf)
	_ = reader.Close()
	if runErr != nil {
		t.Fatalf("printer returned an error: %v", runErr)
	}
	return string(buf[:n])
}

func TestRecipeTouchesNothing(t *testing.T) {
	fakeInit(t, "procd", true, false)
	stateDir := filepath.Join(t.TempDir(), "state")

	capture(t, func() error { return InstallService([]string{"lxd", "--state-dir", stateDir}, "", false) })
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatal("install must not create the state dir")
	}
	if _, err := os.Stat(filepath.Join(stateDir, daemonConfigFile)); !os.IsNotExist(err) {
		t.Fatal("install must not create daemon.json")
	}

	// A state dir that exists must survive uninstall --purge: it only prints
	// the rm command.
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	capture(t, func() error { return UninstallService(true, false, "", false) })
	if _, err := os.Stat(stateDir); err != nil {
		t.Fatal("uninstall --purge must not delete anything")
	}
}

func TestProcdRecipeContents(t *testing.T) {
	fakeInit(t, "procd", true, false)
	out := capture(t, func() error {
		return InstallService([]string{"lxd", "--state-dir", "/etc/sing-box-lxd/state"}, "", false)
	})

	for _, want := range []string{
		docsURL + "#83-openwrt--procd-routers", // ссылка на нужный раздел
		"NOTHING was installed",                // принцип виден оператору
		"USE_PROCD=1",                          // рецепт именно procd
		"procd_set_param respawn",              // служба переживает падение
		"/etc/init.d/sing-box-lxd enable",      // команды включения
		"$(openssl rand -hex 32)",              // секрет генерится на месте — openssl, busybox без xxd
		"client add --name my-launcher",        // шаг сопряжения
		"/etc/sysupgrade.conf",                 // прошивка не съест установку
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("procd recipe must mention %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "systemctl") {
		t.Fatalf("procd recipe must not mention systemctl:\n%s", out)
	}
	// busybox has no xxd: the xxd form would expand to an empty secret. Must
	// not appear in the procd recipe.
	if strings.Contains(out, "xxd") {
		t.Fatalf("procd recipe must not use xxd (absent in busybox):\n%s", out)
	}
	// The pairing step drops --state-dir when it is the platform default the
	// `client` subcommands find anyway.
	if strings.Contains(out, "client add") && strings.Contains(out, "--state-dir /etc/sing-box-lxd/state client add") {
		t.Fatalf("default state dir must not be repeated in the pairing step:\n%s", out)
	}
}

// TestPairingStepKeepsCustomStateDir: a non-default home must be spelled out,
// or the printed command would talk to the wrong daemon.
func TestPairingStepKeepsCustomStateDir(t *testing.T) {
	fakeInit(t, "procd", true, false)
	out := capture(t, func() error {
		return InstallService([]string{"lxd", "--state-dir", "/srv/custom/state"}, "", false)
	})
	if !strings.Contains(out, "--state-dir /srv/custom/state client add") {
		t.Fatalf("custom state dir must stay in the pairing step:\n%s", out)
	}
}

func TestSystemdRecipeContents(t *testing.T) {
	fakeInit(t, "systemd", false, true)
	out := capture(t, func() error {
		return InstallService([]string{"lxd", "--state-dir", "/var/lib/sing-box-lxd/state"}, "", false)
	})

	for _, want := range []string{
		docsURL + "#82-systemd-a-regular-serverdesktop",
		"/etc/systemd/system/sing-box-lxd.service",
		"Restart=always",
		"systemctl enable --now sing-box-lxd",
		"$(head -c 32 /dev/urandom | xxd -p -c 64)", // xxd-форма уместна на systemd-хостах
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("systemd recipe must mention %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "USE_PROCD") {
		t.Fatalf("systemd recipe must not mention procd:\n%s", out)
	}
}

func TestUnknownInitStillHelps(t *testing.T) {
	fakeInit(t, "", false, false)
	out := capture(t, func() error { return InstallService([]string{"lxd", "--state-dir", "/srv/lxd/state"}, "", false) })
	if !strings.Contains(out, "no supported init detected") {
		t.Fatalf("unknown init must say so; got:\n%s", out)
	}
	// Even with no init it must stay useful: the daemon home recipe plus the
	// command line to run under whatever supervises the host.
	if !strings.Contains(out, "/srv/lxd/state") || !strings.Contains(out, "lxd --state-dir /srv/lxd/state") {
		t.Fatalf("unknown init must still print the command line; got:\n%s", out)
	}
}

// TestDryRunChangesNothingHere: on linux every action is already a printout,
// so --dry-run must be a no-op — same bytes either way.
func TestDryRunChangesNothingHere(t *testing.T) {
	fakeInit(t, "procd", true, false)
	args := []string{"lxd", "--state-dir", "/etc/sing-box-lxd/state"}
	plain := capture(t, func() error { return InstallService(args, "", false) })
	dry := capture(t, func() error { return InstallService(args, "", true) })
	if plain != dry {
		t.Fatalf("--dry-run must not change the output on linux:\n--- plain ---\n%s\n--- dry ---\n%s", plain, dry)
	}
}

func TestStateDirDefaultsPerInit(t *testing.T) {
	fakeInit(t, "procd", true, false)
	if got := DefaultServiceStateDir(false); got != "/etc/sing-box-lxd/state" {
		// /var is tmpfs on OpenWrt — state there would not survive a reboot.
		t.Fatalf("openwrt default must be persistent /etc, got %q", got)
	}
	fakeInit(t, "systemd", false, true)
	if got := DefaultServiceStateDir(false); got != "/var/lib/sing-box-lxd/state" {
		t.Fatalf("systemd default must be /var/lib, got %q", got)
	}
}
