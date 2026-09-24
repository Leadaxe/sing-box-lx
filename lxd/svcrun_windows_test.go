//go:build with_lxd && windows

package lxd

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// TestServiceCommandLineRoundTrip: the BinaryPathName install writes
// (ComposeCommandLine) reads back to the same argv, argv[0] quoted when the
// copy's path has a space (SPEC 103 §2.6 p. 6, §2.9).
func TestServiceCommandLineRoundTrip(t *testing.T) {
	arguments := []string{`C:\Program Files\sing-box-lxd\sing-box-lxd.exe`, "lxd", "--state-dir", `C:\ProgramData\sing-box-lxd\state`, "--config-force", `C:\a b\c "d".json`}
	commandLine := windows.ComposeCommandLine(arguments)
	if commandLine[0] != '"' || unquotedPathWithSpace(commandLine) {
		t.Fatalf("argv[0] with a space must be quoted: %s", commandLine)
	}
	decomposed, err := windows.DecomposeCommandLine(commandLine)
	if err != nil {
		t.Fatal(err)
	}
	if len(decomposed) != len(arguments) {
		t.Fatalf("round trip: %q", decomposed)
	}
	for index := range arguments {
		if decomposed[index] != arguments[index] {
			t.Fatalf("argument %d: %q, want %q", index, decomposed[index], arguments[index])
		}
	}
	if !unquotedPathWithSpace(`C:\Program Files\sing-box-lxd\sing-box-lxd.exe lxd`) {
		t.Fatal("an unquoted path with a space must be caught")
	}
	if unquotedPathWithSpace(`C:\sing-box-lxd\sing-box-lxd.exe lxd --state-dir "C:\a b"`) {
		t.Fatal("a space in a later argument is not the executable's")
	}
}

// runHandler drives Execute with scripted requests and collects the
// statuses it reports.
func runHandler(t *testing.T, handler *serviceHandler, requests []svc.ChangeRequest) ([]svc.State, uint32) {
	t.Helper()
	requestChannel := make(chan svc.ChangeRequest)
	statusChannel := make(chan svc.Status, 16)
	result := make(chan uint32, 1)
	go func() {
		_, code := handler.Execute(nil, requestChannel, statusChannel)
		result <- code
	}()
	var states []svc.State
	next := func() svc.State {
		select {
		case status := <-statusChannel:
			states = append(states, status.State)
			return status.State
		case <-time.After(5 * time.Second):
			t.Fatalf("no status after %v", states)
			return 0
		}
	}
	for next() != svc.Running {
	}
	for _, request := range requests {
		requestChannel <- request
		next()
	}
	select {
	case code := <-result:
		for {
			select {
			case status := <-statusChannel:
				states = append(states, status.State)
			default:
				return states, code
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Execute did not return, statuses %v", states)
		return nil, 0
	}
}

func equalStates(first, second []svc.State) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

// TestServiceHandlerExecute: StartPending, Running, Interrogate answered with
// the current status, Stop → StopPending, the body cancelled, Stopped
// (SPEC 103 §2.10); a body that ignores the stop trips the watchdog.
func TestServiceHandlerExecute(t *testing.T) {
	body := func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	}
	running := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	handler := &serviceHandler{
		prepare:  func() (ServiceBody, error) { return body, nil },
		watchdog: time.Second,
		exit:     func(int) { t.Fatal("the watchdog must not fire") },
	}
	states, code := runHandler(t, handler, []svc.ChangeRequest{
		{Cmd: svc.Interrogate, CurrentStatus: running},
		{Cmd: svc.Stop},
	})
	want := []svc.State{svc.StartPending, svc.Running, svc.Running, svc.StopPending, svc.Stopped}
	if code != 0 || !equalStates(states, want) {
		t.Fatalf("statuses %v code %d, want %v code 0", states, code, want)
	}

	// A failed prepare (daemon.json, self-check) stops with exit code 1.
	handler.prepare = func() (ServiceBody, error) { return nil, errors.New("refused") }
	statusChannel := make(chan svc.Status, 4)
	if _, code = handler.Execute(nil, make(chan svc.ChangeRequest), statusChannel); code != 1 {
		t.Fatalf("a failed prepare must exit 1, got %d", code)
	}

	// The body failing on its own is exit code 1 as well.
	handler.prepare = func() (ServiceBody, error) {
		return func(context.Context) error { return errors.New("listen failed") }, nil
	}
	if _, code = handler.Execute(nil, make(chan svc.ChangeRequest), make(chan svc.Status, 4)); code != 1 {
		t.Fatalf("a failed body must exit 1, got %d", code)
	}

	// A body that ignores the stop: the watchdog exits.
	exited := make(chan int, 1)
	handler.prepare = func() (ServiceBody, error) {
		return func(context.Context) error { select {} }, nil
	}
	handler.watchdog = 100 * time.Millisecond
	handler.exit = func(code int) { exited <- code }
	_, code = runHandler(t, handler, []svc.ChangeRequest{{Cmd: svc.Shutdown}})
	if code != 1 || <-exited != 1 {
		t.Fatalf("the watchdog must exit 1, got %d", code)
	}
}
