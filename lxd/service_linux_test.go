//go:build with_lx_command && linux

package lxd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeInit points the probes at a temp root so the detector can be driven
// without the test machine's real init.
func fakeInit(t *testing.T, pid1Comm string, openwrt bool, systemdRun bool) {
	t.Helper()
	root := t.TempDir()
	oldPid1, oldOpenwrt, oldSystemd := pid1CommPath, openwrtReleasePath, systemdRunPath
	t.Cleanup(func() {
		pid1CommPath, openwrtReleasePath, systemdRunPath = oldPid1, oldOpenwrt, oldSystemd
	})

	pid1CommPath = filepath.Join(root, "comm")
	if pid1Comm != "" {
		if err := os.WriteFile(pid1CommPath, []byte(pid1Comm+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	openwrtReleasePath = filepath.Join(root, "openwrt_release")
	if openwrt {
		if err := os.WriteFile(openwrtReleasePath, []byte("DISTRIB_ID='OpenWrt'\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	systemdRunPath = filepath.Join(root, "run-systemd-system")
	if systemdRun {
		if err := os.MkdirAll(systemdRunPath, 0o755); err != nil {
			t.Fatal(err)
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

func TestDetectInitPrefersPID1(t *testing.T) {
	// PID 1 is the honest answer even when the other markers disagree: an
	// OpenWrt userland booted under systemd is systemd's machine.
	fakeInit(t, "systemd", true, false)
	if got := detectInit(); got != initSystemd {
		t.Fatalf("pid1=systemd must win over the openwrt marker, got %v", got)
	}
	fakeInit(t, "procd", false, true)
	if got := detectInit(); got != initProcd {
		t.Fatalf("pid1=procd must win over /run/systemd/system, got %v", got)
	}
	// Unreadable /proc/1/comm (containers, restricted procfs) → file probes.
	fakeInit(t, "", true, false)
	if got := detectInit(); got != initProcd {
		t.Fatalf("openwrt marker must be the fallback, got %v", got)
	}
	fakeInit(t, "", false, true)
	if got := detectInit(); got != initSystemd {
		t.Fatalf("/run/systemd/system must be the fallback, got %v", got)
	}
	fakeInit(t, "", false, false)
	if got := detectInit(); got != initUnknown {
		t.Fatalf("no markers must mean unknown, got %v", got)
	}
}

// TestRecipeTouchesNothing is the principle as a test: --service on linux
// prints and never writes. It must hold for install AND uninstall --purge.
func TestRecipeTouchesNothing(t *testing.T) {
	fakeInit(t, "procd", true, false)
	stateDir := filepath.Join(t.TempDir(), "state")

	capture(t, func() error { return InstallService([]string{"lxd", "--state-dir", stateDir}) })
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
	capture(t, func() error { return UninstallService(true) })
	if _, err := os.Stat(stateDir); err != nil {
		t.Fatal("uninstall --purge must not delete anything")
	}
}

func TestProcdRecipeContents(t *testing.T) {
	fakeInit(t, "procd", true, false)
	out := capture(t, func() error {
		return InstallService([]string{"lxd", "--state-dir", "/etc/sing-box-lxd/state"})
	})

	for _, want := range []string{
		docsURL + "#83-openwrt--procd-routers", // ссылка на нужный раздел
		"NOTHING was installed",                // принцип виден оператору
		"USE_PROCD=1",                          // рецепт именно procd
		"procd_set_param respawn",              // служба переживает падение
		"/etc/init.d/sing-box-lxd enable",      // команды включения
		"$(head -c 32 /dev/urandom",            // секрет генерится на месте
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
		return InstallService([]string{"lxd", "--state-dir", "/srv/custom/state"})
	})
	if !strings.Contains(out, "--state-dir /srv/custom/state client add") {
		t.Fatalf("custom state dir must stay in the pairing step:\n%s", out)
	}
}

func TestSystemdRecipeContents(t *testing.T) {
	fakeInit(t, "systemd", false, true)
	out := capture(t, func() error {
		return InstallService([]string{"lxd", "--state-dir", "/var/lib/sing-box-lxd/state"})
	})

	for _, want := range []string{
		docsURL + "#82-systemd-a-regular-serverdesktop",
		"/etc/systemd/system/sing-box-lxd.service",
		"Restart=always",
		"systemctl enable --now sing-box-lxd",
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
	out := capture(t, func() error { return InstallService([]string{"lxd", "--state-dir", "/srv/lxd/state"}) })
	if !strings.Contains(out, "no supported init detected") {
		t.Fatalf("unknown init must say so; got:\n%s", out)
	}
	// Even with no init it must stay useful: the daemon home recipe plus the
	// command line to run under whatever supervises the host.
	if !strings.Contains(out, "/srv/lxd/state") || !strings.Contains(out, "lxd --state-dir /srv/lxd/state") {
		t.Fatalf("unknown init must still print the command line; got:\n%s", out)
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
