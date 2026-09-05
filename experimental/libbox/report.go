//go:build darwin || linux || windows

package libbox

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"time"

	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
)

type reportMetadata struct {
	Source              string `json:"source,omitempty"`
	BundleIdentifier    string `json:"bundleIdentifier,omitempty"`
	ProcessName         string `json:"processName,omitempty"`
	ProcessPath         string `json:"processPath,omitempty"`
	StartedAt           string `json:"startedAt,omitempty"`
	AppVersion          string `json:"appVersion,omitempty"`
	AppMarketingVersion string `json:"appMarketingVersion,omitempty"`
	CoreVersion         string `json:"coreVersion,omitempty"`
	GoVersion           string `json:"goVersion,omitempty"`
}

func baseReportMetadata() reportMetadata {
	processPath, _ := os.Executable()
	processName := filepath.Base(processPath)
	if processName == "." {
		processName = ""
	}
	return reportMetadata{
		Source:              sCrashReportSource,
		ProcessName:         processName,
		ProcessPath:         processPath,
		AppVersion:          sAppVersion,
		AppMarketingVersion: sAppMarketingVersion,
		CoreVersion:         C.Version,
		GoVersion:           GoVersion(),
	}
}

func writeReportFile(destPath string, name string, content []byte) {
	filePath := filepath.Join(destPath, name)
	os.WriteFile(filePath, content, 0o666)
	chownReport(filePath)
}

func writeReportMetadata(destPath string, metadata any) {
	data, err := json.Marshal(metadata)
	if err != nil {
		return
	}
	writeReportFile(destPath, "metadata.json", data)
}

func copyConfigSnapshot(destPath string) {
	snapshotPath := configSnapshotPath()
	content, err := os.ReadFile(snapshotPath)
	if err != nil {
		return
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return
	}
	writeReportFile(destPath, "configuration.json", content)
}

func initReportDir(path string) {
	os.MkdirAll(path, 0o777)
	chownReport(path)
}

func chownReport(path string) {
	if runtime.GOOS != "android" && runtime.GOOS != "windows" {
		os.Chown(path, sUserID, sGroupID)
	}
}

// lx:begin report-rotation
//
// Report archives are capped so a recurring fault cannot fill the device. Both limits
// apply; whichever bites first wins. An OOM report carries two pprof profiles and a config
// copy (~750 KB each), so the count limit is what usually holds, while the byte budget
// covers the case of a few unusually fat reports.
const (
	maxReportCount = 32
	maxReportBytes = 64 * 1024 * 1024
)

// pruneReports deletes the oldest report directories until the archive fits the limits
// above. Best-effort: it is housekeeping on the crash/OOM path, so any failure is skipped
// rather than propagated — losing a report is better than losing the report that matters.
//
// Ordering is by modification time, NOT by name: collision suffixes (-1..-1000 from
// nextAvailableReportPath) break lexicographic order, since "…-05-2" sorts after
// "…-05-10". Entries whose mtime cannot be read are treated as oldest and go first.
func pruneReports(reportsDir string, keepCount int, keepBytes int64) {
	entries, err := os.ReadDir(reportsDir)
	if err != nil {
		return
	}
	type report struct {
		path     string
		modTime  time.Time
		size     int64
		hasStamp bool
	}
	reports := make([]report, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(reportsDir, entry.Name())
		item := report{path: path}
		if info, infoErr := entry.Info(); infoErr == nil {
			item.modTime = info.ModTime()
			item.hasStamp = true
		}
		item.size = reportDirSize(path)
		reports = append(reports, item)
	}
	sort.SliceStable(reports, func(i, j int) bool {
		if reports[i].hasStamp != reports[j].hasStamp {
			return !reports[i].hasStamp
		}
		return reports[i].modTime.Before(reports[j].modTime)
	})

	var totalBytes int64
	for _, item := range reports {
		totalBytes += item.size
	}
	// Leave room for the report about to be written: prune down to keepCount-1 so the
	// archive holds keepCount once it lands, instead of overshooting by one every time.
	for index, item := range reports {
		remaining := len(reports) - index
		if remaining < keepCount && totalBytes <= keepBytes {
			break
		}
		if os.RemoveAll(item.path) != nil {
			continue
		}
		totalBytes -= item.size
	}
}

func reportDirSize(path string) int64 {
	var total int64
	filepath.WalkDir(path, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// lx:end

func nextAvailableReportPath(reportsDir string, timestamp time.Time) (string, error) {
	destName := timestamp.Format("2006-01-02T15-04-05")
	destPath := filepath.Join(reportsDir, destName)
	_, err := os.Stat(destPath)
	if os.IsNotExist(err) {
		return destPath, nil
	}
	for i := 1; i <= 1000; i++ {
		suffixedPath := filepath.Join(reportsDir, destName+"-"+strconv.Itoa(i))
		_, err = os.Stat(suffixedPath)
		if os.IsNotExist(err) {
			return suffixedPath, nil
		}
	}
	return "", E.New("no available report path for ", destName)
}
