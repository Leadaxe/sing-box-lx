//go:build with_lxd && windows

package lxd

import (
	"context"
	"os"
	"time"

	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/sys/windows/svc"
)

// ServiceBody is the daemon as the SCM handler runs it: until ctx is
// cancelled or it fails on its own.
type ServiceBody func(ctx context.Context) error

// IsWindowsService reports whether the SCM started this process.
func IsWindowsService() bool {
	service, err := svc.IsWindowsService()
	return err == nil && service
}

// RunService hands the process to the SCM (SPEC 103 §2.10). Without it the
// SCM gives up on the start after its timeout (error 1053). prepare runs
// inside the handler — daemon.json, the log, the self-check, the working
// directory — and returns the body; a prepare error stops the service with
// exit code 1, which the SCM records and the recovery actions act on.
func RunService(prepare func() (ServiceBody, error)) error {
	return svc.Run(windowsServiceName, &serviceHandler{
		prepare:  prepare,
		watchdog: serviceStopWatchdog,
		exit:     os.Exit,
	})
}

// serviceStopWatchdog bounds a stop: past it the process exits by itself.
const serviceStopWatchdog = 10 * time.Second

type serviceHandler struct {
	prepare  func() (ServiceBody, error)
	watchdog time.Duration
	exit     func(code int)
}

// Execute reports StartPending, prepares, starts the body and reports
// Running at once — the core's bootstrap may take longer than the SCM's
// start timeout, and the control channel comes up first anyway. Stop and
// Shutdown cancel the body and wait for it under the watchdog.
func (h *serviceHandler) Execute(args []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	statuses <- svc.Status{State: svc.StartPending, WaitHint: 30000}
	body, err := h.prepare()
	if err != nil {
		log.Error(err)
		return false, 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- body(ctx)
	}()
	running := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	statuses <- running
	for {
		select {
		case err = <-done:
			// The daemon ended on its own: a failure is exit code 1, a stop
			// with an error to the SCM, which the recovery restarts.
			if err != nil {
				log.Error(E.Cause(err, "lxd"))
				return false, 1
			}
			return false, 0
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				statuses <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				statuses <- svc.Status{State: svc.StopPending, WaitHint: uint32((h.watchdog + 5*time.Second) / time.Millisecond)}
				cancel()
				select {
				case err = <-done:
					if err != nil {
						log.Error(E.Cause(err, "lxd: stop"))
					}
				case <-time.After(h.watchdog):
					log.Error("lxd: the daemon did not stop within ", h.watchdog, ", exiting")
					h.exit(1)
					return false, 1
				}
				statuses <- svc.Status{State: svc.Stopped}
				return false, 0
			default:
				statuses <- running
			}
		}
	}
}
