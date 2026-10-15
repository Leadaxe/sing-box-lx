//go:build with_lxd

package lxd

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
)

// Named commands (SPEC 128): the operator lists root commands in daemon.json
// and a client runs one BY NAME — never with input of its own. The sudoers
// model: whoever holds root on the host enumerates what the network client
// may fire, and the daemon only picks from that list. The border of SPEC 100
// (a client configures the network but executes no code) holds: the code that
// runs is the operator's, written on the host.
//
// Execution is detached: the command lives in its own session, so a service
// restart — which `update` does — kills the daemon but not the command. The
// wrapper shell writes the exit code itself, so the outcome is on disk whether
// or not the daemon that started it is still around; the daemon derives the
// state from the files, never from memory.

// commandNamePattern keeps a name safe in a URL and a file name.
var commandNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// commandLogTail is how much of a command's log a GET returns.
const commandLogTail = 64 << 10

// validateCommands applies the daemon.json rules: a name is [a-z0-9][a-z0-9_-]{0,31},
// a command is a non-empty string. The daemon refuses to start otherwise, naming
// the offender — a typo must not become a silently missing button.
func validateCommands(commands map[string]string) error {
	for name, command := range commands {
		if !commandNamePattern.MatchString(name) {
			return E.New("daemon.json: commands: invalid name ", strconv.Quote(name), " (want [a-z0-9][a-z0-9_-]{0,31})")
		}
		if command == "" {
			return E.New("daemon.json: commands: ", name, ": empty command")
		}
	}
	return nil
}

// commandState is the lifecycle of the last run of one named command.
const (
	commandIdle    = "idle"    // never run (since the outcome directory was last cleared)
	commandRunning = "running" // started, no exit code yet, pid alive
	commandDone    = "done"    // exit code recorded
	commandLost    = "lost"    // started, no exit code, pid gone — killed before the wrapper could record
)

// commandRun is what the daemon records BEFORE spawning (<name>.json). It is
// the only part of the outcome the daemon writes; the log and the exit code are
// written by the command and its wrapper.
type commandRun struct {
	StartedAt string `json:"started_at"`
	PID       int    `json:"pid"`
	Client    string `json:"client"`
}

// commandView is the REST shape of one command.
type commandView struct {
	Name       string `json:"name"`
	Command    string `json:"command"`
	State      string `json:"state"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	ExitCode   *int   `json:"exit_code"`
	Client     string `json:"client"`
	Log        string `json:"log,omitempty"`
}

// commandRunner holds the configured commands and the outcome directory.
type commandRunner struct {
	commands map[string]string
	// dir is where outcomes live: beside the daemon's log (tmpfs on a router,
	// so a run wears no flash), or the state dir for a terminal run.
	dir string
	// env is the daemon-provided environment for every command (LXD_*).
	env []string
	// spawn starts the wrapper detached and returns its pid; tests swap it.
	spawn func(dir, name, command string, env []string) (int, error)
	// alive reports whether a pid still runs; tests swap it.
	alive func(pid int) bool

	access sync.Mutex
}

// newCommandRunner prepares the outcome directory and clears outcomes of names
// no longer configured, so a renamed command does not show a stranger's result.
func newCommandRunner(commands map[string]string, dir string, env []string) (*commandRunner, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, E.Cause(err, "create command outcome directory ", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, E.Cause(err, "read command outcome directory ", dir)
	}
	for _, entry := range entries {
		name := entry.Name()
		if ext := filepath.Ext(name); ext != "" {
			name = name[:len(name)-len(ext)]
		}
		if _, configured := commands[name]; !configured {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
	return &commandRunner{
		commands: commands,
		dir:      dir,
		env:      env,
		spawn:    spawnDetached,
		alive:    pidAlive,
	}, nil
}

func (r *commandRunner) runPath(name string) string  { return filepath.Join(r.dir, name+".json") }
func (r *commandRunner) logPath(name string) string  { return filepath.Join(r.dir, name+".log") }
func (r *commandRunner) exitPath(name string) string { return filepath.Join(r.dir, name+".exit") }

// view reads the outcome of one command from its files. withLog adds the log
// tail (only the single-command GET carries it).
func (r *commandRunner) view(name string, withLog bool) commandView {
	view := commandView{Name: name, Command: r.commands[name], State: commandIdle}
	content, err := os.ReadFile(r.runPath(name))
	if err != nil {
		return view
	}
	var run commandRun
	if json.Unmarshal(content, &run) != nil {
		return view
	}
	view.StartedAt = run.StartedAt
	view.Client = run.Client
	exitInfo, err := os.Stat(r.exitPath(name))
	switch {
	case err == nil:
		view.State = commandDone
		view.FinishedAt = exitInfo.ModTime().UTC().Format(time.RFC3339)
		if raw, readErr := os.ReadFile(r.exitPath(name)); readErr == nil {
			if code, parseErr := strconv.Atoi(strings.TrimSpace(string(raw))); parseErr == nil {
				view.ExitCode = &code
			}
		}
	case r.alive(run.PID):
		view.State = commandRunning
	default:
		view.State = commandLost
	}
	if withLog {
		view.Log = tailFile(r.logPath(name), commandLogTail)
	}
	return view
}

// start spawns the named command for client. Serialised per runner: the check
// "already running" and the spawn must not race two POSTs into two processes.
func (r *commandRunner) start(name, client string) (commandView, error) {
	r.access.Lock()
	defer r.access.Unlock()
	command, configured := r.commands[name]
	if !configured {
		return commandView{}, errCommandUnknown
	}
	if current := r.view(name, false); current.State == commandRunning {
		return current, errCommandRunning
	}
	_ = os.Remove(r.exitPath(name))
	_ = os.Remove(r.logPath(name))
	env := append([]string{"LXD_CLIENT=" + client}, r.env...)
	pid, err := r.spawn(r.dir, name, command, env)
	if err != nil {
		return commandView{}, E.Cause(err, "start command ", name)
	}
	run := commandRun{StartedAt: nowStamp(), PID: pid, Client: client}
	encoded, _ := json.Marshal(run)
	if err := os.WriteFile(r.runPath(name), encoded, 0o600); err != nil {
		return commandView{}, E.Cause(err, "record command ", name)
	}
	log.Info("lxd: command ", name, " started by ", client, " (pid ", pid, ")")
	return r.view(name, false), nil
}

var (
	errCommandUnknown = errors.New("no such command")
	errCommandRunning = errors.New("command is already running")
)

// pidAlive: signal 0 reaches a live process (or one we may not signal — still alive).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission)
}

// tailFile returns the last limit bytes of a file, "" when absent.
func tailFile(path string, limit int64) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	if info.Size() > limit {
		if _, err := file.Seek(info.Size()-limit, io.SeekStart); err != nil {
			return ""
		}
	}
	content, err := io.ReadAll(file)
	if err != nil {
		return ""
	}
	return string(content)
}

// commandEnv is the daemon-provided environment of every command: facts about
// the host from the daemon, never input from the client. PATH is set when the
// daemon has none (procd starts services with a bare environment).
func commandEnv(platform platformInfo, stateDir, executable string) []string {
	env := []string{
		"LXD_PLATFORM=" + platform.platform,
		"LXD_VERSION=" + C.Version,
		"LXD_INIT_SYSTEM=" + platform.initSystem,
		"LXD_STATE_DIR=" + stateDir,
		"LXD_BIN=" + executable,
	}
	if os.Getenv("PATH") == "" {
		env = append(env, "PATH=/usr/sbin:/usr/bin:/sbin:/bin")
	}
	return env
}

// commandOutcomeDir is where outcomes live: beside the daemon's log when it
// has one (tmpfs on a router — a run wears no flash), else in the state dir.
func commandOutcomeDir(logFile, stateDir string) string {
	if logFile != "" {
		return filepath.Join(filepath.Dir(logFile), "commands")
	}
	return filepath.Join(stateDir, "commands")
}

// ---- REST ----

func (c *controller) handleCommandList(writer http.ResponseWriter, request *http.Request) {
	names := make([]string, 0, len(c.commands.commands))
	for name := range c.commands.commands {
		names = append(names, name)
	}
	sort.Strings(names)
	views := make([]commandView, 0, len(names))
	for _, name := range names {
		views = append(views, c.commands.view(name, false))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"commands": views})
}

func (c *controller) handleCommandGet(writer http.ResponseWriter, request *http.Request) {
	name := request.PathValue("name")
	if _, configured := c.commands.commands[name]; !configured {
		writeJSON(writer, http.StatusNotFound, map[string]any{"error": "no such command"})
		return
	}
	writeJSON(writer, http.StatusOK, c.commands.view(name, true))
}

// handleCommandStart: the contract "a client passes nothing" is enforced, not
// documented — a non-empty body is a 400.
func (c *controller) handleCommandStart(writer http.ResponseWriter, request *http.Request) {
	name := request.PathValue("name")
	if request.Body != nil {
		probe := make([]byte, 1)
		if n, _ := request.Body.Read(probe); n > 0 {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "commands take no input"})
			return
		}
	}
	view, err := c.commands.start(name, clientNameOf(request, c.clients))
	switch {
	case errors.Is(err, errCommandUnknown):
		writeJSON(writer, http.StatusNotFound, map[string]any{"error": "no such command"})
	case errors.Is(err, errCommandRunning):
		writeJSON(writer, http.StatusConflict, view)
	case err != nil:
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": err.Error()})
	default:
		writeJSON(writer, http.StatusAccepted, view)
	}
}

// clientNameOf names the caller for the audit line and LXD_CLIENT: the trusted
// client's name by certificate, "operator" over the Bearer path.
func clientNameOf(request *http.Request, clients *clientRegistry) string {
	if clients != nil && request.TLS != nil && len(request.TLS.PeerCertificates) > 0 {
		if name, found := clients.nameOf(fingerprintOf(request.TLS.PeerCertificates[0].Raw)); found {
			return name
		}
	}
	return "operator"
}
