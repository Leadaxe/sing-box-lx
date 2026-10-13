//go:build with_lxd

package lxd

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
)

// Self-update (SPEC 129): `sing-box lxd update` replaces the running binary
// with the latest release for ITS platform, verified against the release's
// SHA256SUMS, and restarts the service through the host's init. No code is
// downloaded — only the archive, and the sum is checked before anything on
// disk changes. The process is independent of the daemon: started by a named
// command (SPEC 128) it survives the restart it causes; started by hand over
// SSH it prints the same lines.
//
// The pieces are pure and the environment is injected (updateEnv), so the
// whole flow runs in a test on any host against a synthetic archive.

// updateRepo is the trust anchor: the asset and its sum come from one release
// of this repository. Deliberately not configurable — a mirror would be a
// second anchor and a separate decision.
const updateRepo = "Leadaxe/sing-box-lx"

// updateDefaultService is the init unit/script name the recipes install.
const updateDefaultService = "sing-box-lxd"

// updateDownloadTimeout bounds the whole download; a router behind a slow
// uplink pulls ~20 MiB.
const updateDownloadTimeout = 10 * time.Minute

// UpdateOptions are the command's flags.
type UpdateOptions struct {
	DryRun     bool
	Prerelease bool
	NoRestart  bool
	// Service is the init unit/script name; default sing-box-lxd.
	Service string
	// StateDir locates last_good.json for the `check` of the new binary.
	StateDir string
}

// Exit codes of `lxd update`.
const (
	UpdateExitOK         = 0 // updated, or already up to date
	UpdateExitFailed     = 1 // nothing changed
	UpdateExitRolledBack = 2 // the new daemon did not come up; the old binary is back
)

// updateEnv is everything the flow reads from the world; tests fake it.
type updateEnv struct {
	goos       string
	euid       int
	executable string // the running binary, symlinks resolved — the file to replace
	version    string // C.Version
	platform   string // release asset suffix (SPEC 127)
	initSystem string // host's init (SPEC 127)
	out        io.Writer
	// fetch GETs a URL; the caller closes the body. size is the response's
	// Content-Length, -1 when the server did not say.
	fetch func(url string) (body io.ReadCloser, size int64, err error)
	// run executes a program with arguments and returns its combined output.
	run func(name string, args ...string) (string, error)
	// freeBytes is the free space on the filesystem holding dir.
	freeBytes func(dir string) (uint64, error)
	sleep     func(time.Duration)
	// now is the clock behind the download progress lines; nil = time.Now.
	now func() time.Time
	// coreStatus asks the (new) daemon whether the core is up — /admin/status
	// over loopback. nil = the probe is unavailable; coreStatusSkip says why.
	coreStatus     func() (CoreStatus, error)
	coreStatusSkip string
}

// updateCoreWait bounds the wait for the core inside the restarted daemon
// (§2.2 step 8b): the daemon itself retries a busy TUN for 10 s, so 20 s
// covers that plus a slow start on a router.
const updateCoreWait = 20 * time.Second

func (env updateEnv) say(format string, args ...any) {
	fmt.Fprintf(env.out, "update: "+format+"\n", args...)
}

// progressInterval bounds the download progress lines to one per second:
// the log is a file the daemon serves by its tail and the launcher polls
// once a second, so a line per second is what a reader can see, and a
// 15 MiB archive on a router leaves tens of lines, not thousands.
const progressInterval = time.Second

// progress wraps the download body so the archive's bytes are reported as
// they stream in: `downloading <name>: 3.2 / 14.1 MiB (23%)`, or only the
// count when the server gave no Content-Length. Nothing is printed when the
// download finishes within one tick; "sha256 match" follows anyway.
func (env updateEnv) progress(body io.Reader, name string, total int64) io.Reader {
	now := env.now
	if now == nil {
		now = time.Now
	}
	return &progressReader{Reader: body, env: env, name: name, total: total, now: now, last: now()}
}

type progressReader struct {
	io.Reader
	env   updateEnv
	name  string
	total int64
	done  int64
	now   func() time.Time
	last  time.Time
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.done += int64(n)
	if n > 0 {
		if at := r.now(); at.Sub(r.last) >= progressInterval {
			r.last = at
			r.env.say("downloading %s: %s", r.name, formatProgress(r.done, r.total))
		}
	}
	return n, err
}

func formatProgress(done, total int64) string {
	mib := func(n int64) string { return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) }
	if total <= 0 {
		return mib(done) + " MiB"
	}
	return mib(done) + " / " + mib(total) + " MiB (" + strconv.FormatInt(done*100/total, 10) + "%)"
}

// lxVersion is a fork version: upstream semver, the lx build number, and an
// optional rc. "1.14.3-lx.16" and "1.14.3-lx.16-rc.2".
type lxVersion struct {
	major, minor, patch, lx, rc int
	hasRC                       bool
}

var lxVersionPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)-lx\.(\d+)(?:-rc\.(\d+))?$`)

func parseLxVersion(text string) (lxVersion, error) {
	match := lxVersionPattern.FindStringSubmatch(strings.TrimSpace(text))
	if match == nil {
		return lxVersion{}, E.New("not a sing-box-lx version: ", strconv.Quote(text))
	}
	var version lxVersion
	version.major, _ = strconv.Atoi(match[1])
	version.minor, _ = strconv.Atoi(match[2])
	version.patch, _ = strconv.Atoi(match[3])
	version.lx, _ = strconv.Atoi(match[4])
	if match[5] != "" {
		version.hasRC = true
		version.rc, _ = strconv.Atoi(match[5])
	}
	return version, nil
}

// compareLxVersion orders by upstream semver, then the lx number, then the
// rc: a release without rc is newer than any rc of the same number.
func compareLxVersion(a, b lxVersion) int {
	for _, pair := range [][2]int{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}, {a.lx, b.lx}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.hasRC && !b.hasRC:
		return -1
	case !a.hasRC && b.hasRC:
		return 1
	case a.rc < b.rc:
		return -1
	case a.rc > b.rc:
		return 1
	}
	return 0
}

// releaseAsset is one line of SHA256SUMS that matches this platform.
type releaseAsset struct {
	name    string // sing-box-1.14.3-lx.16-linux-arm64.tar.gz
	sha256  string
	version string // 1.14.3-lx.16
}

// parseChecksums finds the asset of platform in a SHA256SUMS file. The name
// is matched as a whole — a substring match could hit a neighbouring asset
// (linux-mips vs linux-mipsle).
func parseChecksums(content, platform string) (releaseAsset, error) {
	suffix := "-" + platform + ".tar.gz"
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if !strings.HasPrefix(name, "sing-box-") || !strings.HasSuffix(name, suffix) {
			continue
		}
		version := strings.TrimSuffix(strings.TrimPrefix(name, "sing-box-"), suffix)
		if _, err := parseLxVersion(version); err != nil {
			continue
		}
		return releaseAsset{name: name, sha256: strings.ToLower(fields[0]), version: version}, nil
	}
	return releaseAsset{}, E.New("no asset for platform ", platform, " in SHA256SUMS")
}

// extractBinary streams a .tar.gz into dst: sha256 over the compressed bytes,
// gunzip, tar, and only the entry named sing-box is written — nothing lands
// in a temp dir, nothing is held in memory. The stream is drained to the end
// so the sum covers the whole archive; a mismatch removes dst.
func extractBinary(archive io.Reader, dst, wantSum string) error {
	hasher := sha256.New()
	tee := io.TeeReader(archive, hasher)
	gunzip, err := gzip.NewReader(tee)
	if err != nil {
		return E.Cause(err, "open archive")
	}
	reader := tar.NewReader(gunzip)
	found := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return E.Cause(err, "read archive")
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != "sing-box" {
			continue
		}
		if found {
			return E.New("archive holds more than one sing-box")
		}
		found = true
		if err := writeFileFrom(dst, reader); err != nil {
			return err
		}
	}
	// Drain: the tar reader stops at the end-of-archive marker, the gzip
	// trailer and any padding still count toward the published sum.
	if _, err := io.Copy(io.Discard, tee); err != nil {
		_ = os.Remove(dst)
		return E.Cause(err, "read archive")
	}
	if !found {
		return E.New("no sing-box binary in the archive")
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); got != wantSum {
		_ = os.Remove(dst)
		return E.New("sha256 MISMATCH (expected ", wantSum, ", got ", got, ")")
	}
	return nil
}

func writeFileFrom(dst string, content io.Reader) error {
	file, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return E.Cause(err, "create ", dst)
	}
	if _, err := io.Copy(file, content); err != nil {
		_ = file.Close()
		_ = os.Remove(dst)
		return E.Cause(err, "write ", dst)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(dst)
		return E.Cause(err, "write ", dst)
	}
	return nil
}

// serviceCommands are the init's restart and "is it running" commands for a
// service name; ok=false when the init is not one we know how to drive.
func serviceCommands(initSystem, service string) (restart, running []string, ok bool) {
	switch initSystem {
	case initNameProcd:
		script := "/etc/init.d/" + service
		return []string{script, "restart"}, []string{script, "running"}, true
	case initNameSystemd:
		return []string{"systemctl", "restart", service}, []string{"systemctl", "is-active", "--quiet", service}, true
	case initNameOpenRC:
		return []string{"rc-service", service, "restart"}, []string{"rc-service", service, "status"}, true
	}
	return nil, nil, false
}

// latestReleaseTag resolves the newest release including pre-releases through
// the API; the stable path needs no API at all (releases/latest/download/…).
func latestReleaseTag(env updateEnv) (string, error) {
	body, _, err := env.fetch("https://api.github.com/repos/" + updateRepo + "/releases?per_page=1")
	if err != nil {
		return "", E.Cause(err, "list releases")
	}
	defer body.Close()
	var releases []struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&releases); err != nil {
		return "", E.Cause(err, "parse release list")
	}
	if len(releases) == 0 || releases[0].TagName == "" {
		return "", E.New("the release list is empty")
	}
	return releases[0].TagName, nil
}

func fetchText(env updateEnv, url string) (string, error) {
	body, _, err := env.fetch(url)
	if err != nil {
		return "", err
	}
	defer body.Close()
	content, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// runUpdate is the whole flow (SPEC 129 §2.2); it returns the exit code and
// the error to print on stderr.
func runUpdate(env updateEnv, options UpdateOptions) (int, error) {
	if env.goos != "linux" {
		return UpdateExitFailed, E.New("lxd update works on linux only; on macOS run `sudo <new binary> lxd --service=install`, on Windows use the launcher's installer")
	}
	if env.euid != 0 {
		return UpdateExitFailed, E.New("lxd update must run as root (it replaces ", env.executable, " and restarts the service)")
	}
	if options.Service == "" {
		options.Service = updateDefaultService
	}
	target := env.executable
	newPath, oldPath := target+".new", target+".old"
	// Leftovers of a run that died midway.
	_ = os.Remove(newPath)
	_ = os.Remove(oldPath)

	// 2. What is available.
	base := "https://github.com/" + updateRepo + "/releases/latest/download"
	if options.Prerelease {
		tag, err := latestReleaseTag(env)
		if err != nil {
			return UpdateExitFailed, err
		}
		base = "https://github.com/" + updateRepo + "/releases/download/" + tag
	}
	sums, err := fetchText(env, base+"/SHA256SUMS")
	if err != nil {
		return UpdateExitFailed, E.Cause(err, "download SHA256SUMS")
	}
	asset, err := parseChecksums(sums, env.platform)
	if err != nil {
		return UpdateExitFailed, err
	}
	env.say("installed %s, available %s (%s)", env.version, asset.version, env.platform)
	available, _ := parseLxVersion(asset.version)
	if installed, err := parseLxVersion(env.version); err != nil {
		env.say("installed version is not a release build; comparing nothing")
	} else {
		switch compareLxVersion(available, installed) {
		case 0:
			env.say("already up to date")
			return UpdateExitOK, nil
		case -1:
			return UpdateExitFailed, E.New("installed ", env.version, " is newer than ", asset.version, " (pre-release builds: --prerelease)")
		}
	}

	// 3. Room for one more copy beside the running one.
	targetInfo, err := os.Stat(target)
	if err != nil {
		return UpdateExitFailed, E.Cause(err, "stat ", target)
	}
	need := uint64(targetInfo.Size()) + uint64(targetInfo.Size())/10
	free, err := env.freeBytes(filepath.Dir(target))
	if err != nil {
		return UpdateExitFailed, E.Cause(err, "free space of ", filepath.Dir(target))
	}
	if free < need {
		return UpdateExitFailed, E.New("need ", need>>10, " KiB free beside ", target, ", have ", free>>10)
	}
	if options.DryRun {
		env.say("dry run: would download %s into %s and restart %s via %s", asset.name, newPath, options.Service, env.initSystem)
		return UpdateExitOK, nil
	}

	// 4. Stream the archive into <target>.new; the sum is checked after.
	env.say("downloading %s", asset.name)
	body, size, err := env.fetch("https://github.com/" + updateRepo + "/releases/download/v" + asset.version + "/" + asset.name)
	if err != nil {
		return UpdateExitFailed, E.Cause(err, "download ", asset.name)
	}
	err = extractBinary(env.progress(body, asset.name, size), newPath, asset.sha256)
	_ = body.Close()
	if err != nil {
		_ = os.Remove(newPath)
		return UpdateExitFailed, err
	}
	env.say("sha256 match")

	// 5. The new binary must run here and accept the current config.
	if output, err := env.run(newPath, "version"); err != nil {
		_ = os.Remove(newPath)
		return UpdateExitFailed, E.Cause(err, "the new binary does not run: ", strings.TrimSpace(output))
	}
	lastGood := filepath.Join(options.StateDir, "last_good.json")
	if options.StateDir == "" {
		env.say("no state dir given, skipping check")
	} else if _, err := os.Stat(lastGood); err != nil {
		env.say("no applied config, skipping check")
	} else if output, err := env.run(newPath, "check", "--disable-color", "-c", lastGood); err != nil {
		_ = os.Remove(newPath)
		return UpdateExitFailed, E.Cause(err, "the new binary rejects the config: ", strings.TrimSpace(output))
	}

	// 6. Swap: the hard link keeps the old inode at zero cost; the rename is
	// atomic on the same filesystem — no ETXTBSY, no half-written file.
	if err := os.Link(target, oldPath); err != nil {
		_ = os.Remove(newPath)
		return UpdateExitFailed, E.Cause(err, "keep the old binary")
	}
	if err := os.Rename(newPath, target); err != nil {
		_ = os.Remove(newPath)
		_ = os.Remove(oldPath)
		return UpdateExitFailed, E.Cause(err, "replace ", target)
	}
	env.say("binary replaced: %s is %s", target, asset.version)

	// 7. Restart through the init. A failing restart command is not a bad
	// binary: keep it, tell the operator, exit 0.
	restart, running, known := serviceCommands(env.initSystem, options.Service)
	if options.NoRestart || !known {
		_ = os.Remove(oldPath)
		env.say("restart the service yourself to run %s", asset.version)
		return UpdateExitOK, nil
	}
	env.say("restarting %s via %s", options.Service, env.initSystem)
	if output, err := env.run(restart[0], restart[1:]...); err != nil {
		_ = os.Remove(oldPath)
		env.say("restart failed (%s); restart the service yourself to run %s", strings.TrimSpace(output+" "+err.Error()), asset.version)
		return UpdateExitOK, nil
	}

	// 8. Health: up within 15 s and still up 3 s later (a crash loop under
	// procd flickers); otherwise the old inode goes back.
	healthy := false
	for i := 0; i < 15; i++ {
		env.sleep(time.Second)
		if _, err := env.run(running[0], running[1:]...); err == nil {
			env.sleep(3 * time.Second)
			if _, err := env.run(running[0], running[1:]...); err == nil {
				healthy = true
			}
			break
		}
	}
	failure := asset.version + " did not come up"
	// 8b. The init's "running" is the daemon process, not the core: a daemon
	// that lost the TUN race on restart stays up with a control channel and
	// no VPN (seen live, 2026-10-12). Ask the daemon itself and wait for
	// "started"; a fatal core is a health failure like a dead process.
	if healthy {
		if coreErr := env.waitForCore(); coreErr != nil {
			healthy = false
			failure = asset.version + " is up but the core did not start: " + coreErr.Error()
		}
	}
	if healthy {
		_ = os.Remove(oldPath)
		env.say("done: %s is running", asset.version)
		return UpdateExitOK, nil
	}
	env.say("%s, rolling back to %s", failure, env.version)
	if err := os.Rename(oldPath, target); err != nil {
		return UpdateExitRolledBack, E.Cause(err, "roll back ", target, " (the old binary is at ", oldPath, ")")
	}
	_, _ = env.run(restart[0], restart[1:]...)
	return UpdateExitRolledBack, E.New(failure, ", rolled back to ", env.version)
}

// waitForCore polls /admin/status once a second for up to updateCoreWait.
// "started" is success; "fatal" is final at once (the daemon has given up —
// it retries a busy device itself before saying so); "idle" means the core
// is still starting (bootstrap runs behind the listener). A probe that never
// gets an answer is not a verdict: the init says the process is up, and an
// unreachable or misconfigured admin plane must not trigger a rollback, so
// it is reported and the update stands. No probe at all (no daemon.json,
// control channel not on loopback, core stopped by the operator) says why.
func (env updateEnv) waitForCore() error {
	if env.coreStatus == nil {
		env.say("core status not checked: %s", env.coreStatusSkip)
		return nil
	}
	var lastErr error
	last := CoreStatus{}
	for attempt := 0; attempt <= int(updateCoreWait/time.Second); attempt++ {
		if attempt > 0 {
			env.sleep(time.Second)
		}
		status, err := env.coreStatus()
		if err != nil {
			lastErr = err
			continue
		}
		lastErr = nil
		last = status
		switch status.Status {
		case "started":
			env.say("core started")
			return nil
		case "fatal":
			return E.New(status.LastError)
		}
	}
	if lastErr != nil {
		env.say("core status unknown (%s); the service is running, not rolling back", lastErr)
		return nil
	}
	if last.LastError != "" {
		return E.New("still ", last.Status, " after ", updateCoreWait, ": ", last.LastError)
	}
	return E.New("still ", last.Status, " after ", updateCoreWait)
}

// coreProbe builds the /admin/status probe for the host's daemon from its
// state dir, or explains why there is none.
func coreProbe(stateDir string) (func() (CoreStatus, error), string) {
	if stateDir == "" {
		return nil, "no state dir"
	}
	config, found, err := LoadDaemonConfig(stateDir)
	if err != nil {
		return nil, "daemon.json: " + err.Error()
	}
	if !found {
		return nil, "no daemon.json in " + stateDir
	}
	if stateStore, err := newStore(stateDir); err == nil {
		if running, recorded := stateStore.WasRunning(); recorded && !running {
			return nil, "the core is stopped by the operator"
		}
	}
	address := loopbackAdminAddress(config.Listen)
	if address == "" {
		return nil, "control channel is not on loopback (" + config.Listen.Advertise() + ")"
	}
	client := NewLocalClient(address, config.Secret, config.TLS)
	return client.Status, ""
}

// loopbackAdminAddress picks a control-channel address the host can reach
// as a loopback peer (the operator route's condition): a loopback bind as is,
// an unspecified bind (0.0.0.0, ::) through 127.0.0.1; a daemon bound only
// to LAN addresses has none.
func loopbackAdminAddress(listen ListenConfig) string {
	for _, address := range listen.Addresses() {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			continue
		}
		if host == "" {
			return net.JoinHostPort("127.0.0.1", port)
		}
		ip, err := netip.ParseAddr(host)
		if err != nil {
			continue
		}
		if ip.IsLoopback() {
			return address
		}
		if ip.IsUnspecified() {
			if ip.Is6() {
				return net.JoinHostPort("::1", port)
			}
			return net.JoinHostPort("127.0.0.1", port)
		}
	}
	return ""
}

// Update runs `lxd update` against the real host (SPEC 129).
func Update(options UpdateOptions, out io.Writer) (int, error) {
	executable, err := resolveOwnExecutable()
	if err != nil {
		return UpdateExitFailed, err
	}
	initSystem, _ := detectInitSystem(platformInitSystemEnv())
	client := &http.Client{Timeout: updateDownloadTimeout}
	env := updateEnv{
		goos:       runtimeGOOS,
		euid:       os.Geteuid(),
		executable: executable,
		version:    C.Version,
		platform:   releasePlatform(runtimeGOOS, runtimeGOARCH, buildSettings()),
		initSystem: initSystem,
		out:        out,
		fetch: func(url string) (io.ReadCloser, int64, error) {
			request, err := http.NewRequest(http.MethodGet, url, nil)
			if err != nil {
				return nil, -1, err
			}
			request.Header.Set("User-Agent", "sing-box-lx/"+C.Version+" lxd update")
			response, err := client.Do(request)
			if err != nil {
				return nil, -1, err
			}
			if response.StatusCode != http.StatusOK {
				_ = response.Body.Close()
				return nil, -1, E.New("HTTP ", response.Status, " for ", url)
			}
			return response.Body, response.ContentLength, nil
		},
		run:       runCombined,
		freeBytes: freeBytes,
		sleep:     time.Sleep,
	}
	env.coreStatus, env.coreStatusSkip = coreProbe(options.StateDir)
	return runUpdate(env, options)
}
