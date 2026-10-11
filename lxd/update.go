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
	"net/http"
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
	// fetch GETs a URL; the caller closes the body.
	fetch func(url string) (io.ReadCloser, error)
	// run executes a program with arguments and returns its combined output.
	run func(name string, args ...string) (string, error)
	// freeBytes is the free space on the filesystem holding dir.
	freeBytes func(dir string) (uint64, error)
	sleep     func(time.Duration)
}

func (env updateEnv) say(format string, args ...any) {
	fmt.Fprintf(env.out, "update: "+format+"\n", args...)
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
	body, err := env.fetch("https://api.github.com/repos/" + updateRepo + "/releases?per_page=1")
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
	body, err := env.fetch(url)
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
	body, err := env.fetch("https://github.com/" + updateRepo + "/releases/download/v" + asset.version + "/" + asset.name)
	if err != nil {
		return UpdateExitFailed, E.Cause(err, "download ", asset.name)
	}
	err = extractBinary(body, newPath, asset.sha256)
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
	if healthy {
		_ = os.Remove(oldPath)
		env.say("done: %s is running", asset.version)
		return UpdateExitOK, nil
	}
	env.say("%s did not come up, rolling back to %s", asset.version, env.version)
	if err := os.Rename(oldPath, target); err != nil {
		return UpdateExitRolledBack, E.Cause(err, "roll back ", target, " (the old binary is at ", oldPath, ")")
	}
	_, _ = env.run(restart[0], restart[1:]...)
	return UpdateExitRolledBack, E.New(asset.version, " did not come up, rolled back to ", env.version)
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
		fetch: func(url string) (io.ReadCloser, error) {
			request, err := http.NewRequest(http.MethodGet, url, nil)
			if err != nil {
				return nil, err
			}
			request.Header.Set("User-Agent", "sing-box-lx/"+C.Version+" lxd update")
			response, err := client.Do(request)
			if err != nil {
				return nil, err
			}
			if response.StatusCode != http.StatusOK {
				_ = response.Body.Close()
				return nil, E.New("HTTP ", response.Status, " for ", url)
			}
			return response.Body, nil
		},
		run:       runCombined,
		freeBytes: freeBytes,
		sleep:     time.Sleep,
	}
	return runUpdate(env, options)
}
