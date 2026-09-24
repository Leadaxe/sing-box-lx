//go:build with_lxd

package lxd

import (
	"strings"
	"testing"

	E "github.com/sagernet/sing/common/exceptions"
)

// TestSelfCheckContexts drives the shared decision of SPEC 103 §2.11 with
// the Windows vocabulary and a faked invariant, so the Windows contexts are
// checked on any CI host.
func TestSelfCheckContexts(t *testing.T) {
	const (
		protected = `C:\Program Files\sing-box-lxd\sing-box-lxd.exe`
		launcher  = `C:\Users\u\AppData\Local\singbox-launcher\bin\sing-box.exe`
	)
	check := func(path string) error {
		if path == protected {
			return nil
		}
		return E.New(`C:\Users\u: owner HOST\u (S-1-5-21-1) is not SYSTEM, Administrators or TrustedInstaller`)
	}
	env := func(privileged, service bool, executable string) selfCheckEnv {
		return selfCheckEnv{
			privileged:     privileged,
			serviceContext: service,
			context:        "elevated, outside the SCM",
			executable:     func() (string, error) { return executable, nil },
			check:          check,
			subject:        func(path string) string { return path + " (owner HOST\\u (S-1-5-21-1))" },
			words: selfCheckWords{
				refuseAs:   "a Windows service",
				runningAs:  "running elevated",
				risk:       "anyone who can replace that file or a library beside it runs code as an administrator",
				notService: "not the Windows service",
				remedy:     "run `sing-box lxd --service=install` to reinstall from a protected copy",
				okKind:     "protected",
				service:    "windows service sing-box-lxd",
			},
		}
	}
	for _, testCase := range []struct {
		name        string
		env         selfCheckEnv
		allowUnsafe bool
		wantInfo    string
		wantWarn    string
		wantErr     string
	}{
		{"not elevated: nothing to check", env(false, false, launcher), false, "", "", ""},
		{"service from the protected copy logs INFO", env(true, true, protected), false,
			`lxd: self-check ok: protected ` + protected + `, windows service sing-box-lxd`, "", ""},
		{"elevated run from the protected copy is silent", env(true, false, protected), false, "", "", ""},
		{"service from the launcher dir refuses", env(true, true, launcher), false, "", "",
			"lxd: refusing to run as a Windows service from " + launcher + ` (owner HOST\u (S-1-5-21-1)): C:\Users\u: owner HOST\u (S-1-5-21-1) is not SYSTEM, Administrators or TrustedInstaller; run ` + "`sing-box lxd --service=install`" + ` to reinstall from a protected copy`},
		{"elevated classic run warns", env(true, false, launcher), false, "",
			"lxd: running elevated from " + launcher, ""},
		{"--allow-unsafe-exec warns in the service", env(true, true, launcher), true, "",
			"lxd: --allow-unsafe-exec: running elevated from " + launcher, ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			info, warning, err := evaluateSelfCheck(testCase.env, testCase.allowUnsafe)
			if info != testCase.wantInfo {
				t.Fatalf("info %q, want %q", info, testCase.wantInfo)
			}
			if (testCase.wantWarn == "") != (warning == "") || !strings.Contains(warning, testCase.wantWarn) {
				t.Fatalf("warning %q, want %q", warning, testCase.wantWarn)
			}
			if testCase.wantErr == "" && err != nil || testCase.wantErr != "" && (err == nil || err.Error() != testCase.wantErr) {
				t.Fatalf("error %v, want %q", err, testCase.wantErr)
			}
		})
	}
	// The warning of an elevated run outside the service names the context.
	_, warning, _ := evaluateSelfCheck(env(true, false, launcher), false)
	if !strings.Contains(warning, "not the Windows service (elevated, outside the SCM), starting anyway; the service would refuse this binary") {
		t.Fatalf("warning %q", warning)
	}
}
