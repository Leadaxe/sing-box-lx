package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	_ "github.com/sagernet/gomobile"
	"github.com/sagernet/sing-box/cmd/internal/build_shared"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/rw"
	"github.com/sagernet/sing/common/shell"
)

var (
	debugEnabled bool
	target       string
	platform     string
	// withTailscale bool
)

func init() {
	flag.BoolVar(&debugEnabled, "debug", false, "enable debug")
	flag.StringVar(&target, "target", "android", "target platform")
	flag.StringVar(&platform, "platform", "", "specify platform")
	// flag.BoolVar(&withTailscale, "with-tailscale", false, "build tailscale for iOS and tvOS")
}

func main() {
	flag.Parse()

	build_shared.FindMobile()

	switch target {
	case "android":
		buildAndroid()
	case "apple":
		buildApple()
	}
}

var (
	sharedFlags []string
	debugFlags  []string
	sharedTags  []string
	darwinTags  []string
	// memcTags    []string
	notMemcTags []string
	debugTags   []string
)

func init() {
	sharedFlags = append(sharedFlags, "-trimpath")
	sharedFlags = append(sharedFlags, "-buildvcs=false")
	currentTag, err := build_shared.ReadTag()
	if err != nil {
		currentTag = "unknown"
	}
	sharedFlags = append(sharedFlags, "-ldflags", "-X github.com/sagernet/sing-box/constant.Version="+currentTag+" -X internal/godebug.defaultGODEBUG=multipathtcp=0 -s -w -buildid=  -checklinkname=0")
	debugFlags = append(debugFlags, "-ldflags", "-X github.com/sagernet/sing-box/constant.Version="+currentTag+" -X internal/godebug.defaultGODEBUG=multipathtcp=0 -checklinkname=0")

	// lx: with_usbip (new in 1.14) is intentionally omitted — USB/IP is a server-side
	// service that contradicts the client-trim philosophy and Android client configs
	// never reference usbip endpoints (a missing tag only yields a stub-registration
	// error if a config uses it, which lx configs won't).
	// lx: with_clash_api is intentionally omitted — LxBox manages the core over the
	// native libbox CommandClient (group/url-test/select/connections streams), so the
	// Clash REST API is dead weight on the client. Without the tag a config referencing
	// experimental.clash_api fails fast with "clash api is not included in this build,
	// rebuild with -tags with_clash_api" (no silent fallback). lx configs won't.
	sharedTags = append(sharedTags, "with_gvisor", "with_quic", "with_wireguard", "with_utls", "with_naive_outbound", "badlinkname", "tfogo_checklinkname0")
	// lx:begin awg,xhttp
	// Promote the two downstream features into the Android AAR. They flow into both
	// the main (SDK23) and legacy (SDK21) variants, since legacy derives from
	// sharedTags via filterTags() in buildAndroid(). Without these tags libbox
	// rejects any wireguard-with-AWG or xhttp config at runtime ("support not built").
	sharedTags = append(sharedTags, "with_xhttp", "with_awg")
	// lx:end awg,xhttp
	// lx:begin lx_command
	// SPEC 014 — bake the libbox command-protocol extensions (URLTestOutbound, GetRules)
	// into the AAR. Without the tag the generated RPCs are still registered but the
	// daemon answers codes.Unimplemented (started_service_command_lx_stub.go), so LxBox's
	// per-node delay test and rule-table screen would fail. CONSTITUTION §3.6 pt.6.
	sharedTags = append(sharedTags, "with_lx_command")
	// lx:end lx_command
	darwinTags = append(darwinTags, "with_dhcp", "grpcnotrace")
	// memcTags = append(memcTags, "with_tailscale")
	// lx:begin no-tailscale
	// Drop Tailscale from the libbox AAR: the client fork has no tailscale endpoints,
	// and tailscale is the single largest dependency by size in the APK. Keeps the AAR
	// aligned with the desktop LX_TAGS set (Makefile.lx). The ts_omit_* tags only trim
	// with_tailscale, so they go with it. Restore the upstream append below to re-enable.
	// sharedTags = append(sharedTags, "with_tailscale", "ts_omit_logtail", "ts_omit_ssh", "ts_omit_drive", "ts_omit_taildrop", "ts_omit_webclient", "ts_omit_doctor", "ts_omit_capture", "ts_omit_kube", "ts_omit_aws", "ts_omit_synology", "ts_omit_bird")
	// lx:end no-tailscale
	notMemcTags = append(notMemcTags, "with_low_memory")
	debugTags = append(debugTags, "debug")
}

type AndroidBuildConfig struct {
	AndroidAPI int
	OutputName string
	Tags       []string
}

func filterTags(tags []string, exclude ...string) []string {
	excludeMap := make(map[string]bool)
	for _, tag := range exclude {
		excludeMap[tag] = true
	}
	var result []string
	for _, tag := range tags {
		if !excludeMap[tag] {
			result = append(result, tag)
		}
	}
	return result
}

func checkJavaVersion() {
	var javaPath string
	javaHome := os.Getenv("JAVA_HOME")
	if javaHome == "" {
		javaPath = "java"
	} else {
		javaPath = filepath.Join(javaHome, "bin", "java")
	}

	javaVersion, err := shell.Exec(javaPath, "--version").ReadOutput()
	if err != nil {
		log.Fatal(E.Cause(err, "check java version"))
	}
	if !strings.Contains(javaVersion, "openjdk 17") {
		log.Fatal("java version should be openjdk 17")
	}
}

func getAndroidBindTarget() string {
	if platform != "" {
		return platform
	} else if debugEnabled {
		return "android/arm64"
	}
	return "android"
}

func buildAndroidVariant(config AndroidBuildConfig, bindTarget string) {
	args := []string{
		"bind",
		"-v",
		"-o", config.OutputName,
		"-target", bindTarget,
		"-androidapi", strconv.Itoa(config.AndroidAPI),
		"-javapkg=io.nekohasekai",
		"-libname=box",
	}

	if !debugEnabled {
		args = append(args, sharedFlags...)
	} else {
		args = append(args, debugFlags...)
	}

	args = append(args, "-tags", strings.Join(config.Tags, ","))
	args = append(args, "./experimental/libbox")

	command := exec.Command(build_shared.GoBinPath+"/gomobile", args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	err := command.Run()
	if err != nil {
		log.Fatal(err)
	}

	copyPath := filepath.Join("..", "sing-box-for-android", "app", "libs")
	if rw.IsDir(copyPath) {
		copyPath, _ = filepath.Abs(copyPath)
		err = rw.CopyFile(config.OutputName, filepath.Join(copyPath, config.OutputName))
		if err != nil {
			log.Fatal(err)
		}
		log.Info("copied ", config.OutputName, " to ", copyPath)
	}
}

func buildAndroid() {
	build_shared.FindSDK()
	checkJavaVersion()

	bindTarget := getAndroidBindTarget()

	// Build main variant (SDK 23)
	mainTags := append([]string{}, sharedTags...)
	// mainTags = append(mainTags, memcTags...)
	if debugEnabled {
		mainTags = append(mainTags, debugTags...)
	}
	buildAndroidVariant(AndroidBuildConfig{
		AndroidAPI: 23,
		OutputName: "libbox.aar",
		Tags:       mainTags,
	}, bindTarget)

	// Build legacy variant (SDK 21, no naive outbound)
	legacyTags := filterTags(sharedTags, "with_naive_outbound")
	// legacyTags = append(legacyTags, memcTags...)
	if debugEnabled {
		legacyTags = append(legacyTags, debugTags...)
	}
	buildAndroidVariant(AndroidBuildConfig{
		AndroidAPI: 21,
		OutputName: "libbox-legacy.aar",
		Tags:       legacyTags,
	}, bindTarget)
}

func buildApple() {
	var bindTarget string
	if platform != "" {
		bindTarget = platform
	} else if debugEnabled {
		bindTarget = "ios"
	} else {
		bindTarget = "ios,iossimulator,tvos,tvossimulator,macos"
	}

	args := []string{
		"bind",
		"-v",
		"-target", bindTarget,
		"-libname=box",
		"-tags-not-macos=with_low_memory",
		"-iosversion=15.0",
		"-macosversion=13.0",
		"-tvosversion=17.0",
	}
	//if !withTailscale {
	//	args = append(args, "-tags-macos="+strings.Join(memcTags, ","))
	//}

	if !debugEnabled {
		args = append(args, sharedFlags...)
	} else {
		args = append(args, debugFlags...)
	}

	tags := append(sharedTags, darwinTags...)
	//if withTailscale {
	//	tags = append(tags, memcTags...)
	//}
	if debugEnabled {
		tags = append(tags, debugTags...)
	}

	args = append(args, "-tags", strings.Join(tags, ","))
	args = append(args, "./experimental/libbox")

	command := exec.Command(build_shared.GoBinPath+"/gomobile", args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	err := command.Run()
	if err != nil {
		log.Fatal(err)
	}

	copyPath := filepath.Join("..", "sing-box-for-apple")
	if rw.IsDir(copyPath) {
		targetDir := filepath.Join(copyPath, "Libbox.xcframework")
		targetDir, _ = filepath.Abs(targetDir)
		os.RemoveAll(targetDir)
		os.Rename("Libbox.xcframework", targetDir)
		log.Info("copied to ", targetDir)
	}
}
