//go:build with_lxd && windows

package lxd

import (
	"errors"
	"fmt"
	"io"
	"time"

	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// The SCM side of the Windows service (SPEC 103 §2.6-§2.9) behind an
// interface, so the state machine runs in tests without a real service
// (service_windows_test.go); systemSCM is the real one.

const (
	windowsServiceName        = execCopyBase
	windowsServiceDisplayName = "sing-box-lx daemon (lxd)"
	windowsServiceDescription = "sing-box-lx control daemon: hosts the core behind the launcher's control channel"
	// windowsServiceSDDL (SPEC 103 §2.6 p. 7): Authenticated Users may read
	// the configuration and the status (0x2008d = READ_CONTROL,
	// SERVICE_QUERY_CONFIG, SERVICE_QUERY_STATUS,
	// SERVICE_ENUMERATE_DEPENDENTS, SERVICE_INTERROGATE) — not start, stop,
	// reconfigure or take the service over.
	windowsServiceSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x2008d;;;AU)"
	// scmSettle bounds each wait for a state change.
	scmSettle = 30 * time.Second
)

// scmServiceInfo is what status reads about the service.
type scmServiceInfo struct {
	exists bool
	// configErr: the configuration is unreadable — access denied included,
	// which alone makes the service UNSAFE (SPEC 103 §2.9).
	configErr  error
	binaryPath string
	startType  uint32
	account    string
	state      svc.State
	statusErr  error
	pid        uint32
	dacl       []aclEntry
	nullDACL   bool
	daclErr    error
}

type scmAPI interface {
	// query reads the service with the rights an unprivileged caller has;
	// the error is the SCM itself not opening (exit code 1).
	query(name string) (scmServiceInfo, error)
	// stop stops a running service and waits until it is STOPPED.
	stop(out io.Writer, name string) error
	// start starts the service and waits until it is RUNNING.
	start(out io.Writer, name string) error
	// configure creates or updates the service: binaryPathName, LocalSystem,
	// automatic start after Tcpip, recovery, the service DACL.
	configure(name, binaryPathName string) error
	// remove stops (waiting) and deletes the service.
	remove(out io.Writer, name string) error
}

type systemSCM struct{}

// query opens the manager with SC_MANAGER_CONNECT and the service with the
// query rights only: mgr.Connect asks for SC_MANAGER_ALL_ACCESS and fails
// without elevation, so the handles are opened here and wrapped.
func (systemSCM) query(name string) (scmServiceInfo, error) {
	var info scmServiceInfo
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return info, E.Cause(err, "open the service manager")
	}
	defer windows.CloseServiceHandle(manager)
	namePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return info, err
	}
	withDACL := true
	handle, err := windows.OpenService(manager, namePointer, windows.SERVICE_QUERY_CONFIG|windows.SERVICE_QUERY_STATUS|windows.READ_CONTROL)
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		withDACL = false
		handle, err = windows.OpenService(manager, namePointer, windows.SERVICE_QUERY_CONFIG|windows.SERVICE_QUERY_STATUS)
	}
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return info, nil
	}
	info.exists = true
	if err != nil {
		info.configErr = err
		return info, nil
	}
	defer windows.CloseServiceHandle(handle)
	service := &mgr.Service{Name: name, Handle: handle}
	config, err := service.Config()
	if err != nil {
		info.configErr = err
	} else {
		info.binaryPath = config.BinaryPathName
		info.startType = config.StartType
		info.account = config.ServiceStartName
	}
	status, err := service.Query()
	if err != nil {
		info.statusErr = err
	} else {
		info.state = status.State
		info.pid = status.ProcessId
	}
	if !withDACL {
		info.daclErr = E.New("READ_CONTROL on the service is denied")
		return info, nil
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		info.daclErr = err
		return info, nil
	}
	facts, err := descriptorFacts(descriptor, securityFacts{})
	info.dacl, info.nullDACL, info.daclErr = facts.DACL, facts.NullDACL, err
	return info, nil
}

func (systemSCM) open(name string) (*mgr.Mgr, *mgr.Service, error) {
	manager, err := mgr.Connect()
	if err != nil {
		return nil, nil, E.Cause(err, "connect to the service manager")
	}
	service, err := manager.OpenService(name)
	if err != nil {
		_ = manager.Disconnect()
		return nil, nil, err
	}
	return manager, service, nil
}

func (scm systemSCM) stop(out io.Writer, name string) error {
	manager, service, err := scm.open(name)
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	defer service.Close()
	return stopAndWait(out, service)
}

func stopAndWait(out io.Writer, service *mgr.Service) error {
	status, err := service.Query()
	if err != nil {
		return E.Cause(err, "query service ", service.Name)
	}
	if status.State == svc.Stopped {
		return nil
	}
	if status.State != svc.StopPending {
		if _, err = service.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return E.Cause(err, "stop service ", service.Name)
		}
	}
	return waitState(out, service, svc.Stopped, "stopping")
}

func (scm systemSCM) start(out io.Writer, name string) error {
	manager, service, err := scm.open(name)
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	defer service.Close()
	if err = service.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return E.Cause(err, "start service ", name)
	}
	return waitState(out, service, svc.Running, "starting")
}

// waitState polls until the service reaches state, telling the operator
// every second, up to scmSettle.
func waitState(out io.Writer, service *mgr.Service, state svc.State, doing string) error {
	started := time.Now()
	lastNotice := 0
	for {
		status, err := service.Query()
		if err != nil {
			return E.Cause(err, "query service ", service.Name)
		}
		if status.State == state {
			return nil
		}
		if state == svc.Running && status.State == svc.Stopped {
			return E.New("service ", service.Name, " stopped while starting (exit code ", status.Win32ExitCode,
				", service exit code ", status.ServiceSpecificExitCode, "); see its log")
		}
		waited := time.Since(started)
		if waited >= scmSettle {
			return E.New("service ", service.Name, " did not finish ", doing, " within ", scmSettle, " (state ", uint32(status.State), ")")
		}
		if seconds := int(waited / time.Second); seconds > lastNotice {
			lastNotice = seconds
			fmt.Fprintf(out, "lxd: %s service %s (%ds)\n", doing, service.Name, seconds)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (scm systemSCM) configure(name, binaryPathName string) error {
	manager, err := mgr.Connect()
	if err != nil {
		return E.Cause(err, "connect to the service manager")
	}
	defer manager.Disconnect()
	config := mgr.Config{
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:        mgr.StartAutomatic,
		ErrorControl:     mgr.ErrorNormal,
		BinaryPathName:   binaryPathName,
		Dependencies:     []string{"Tcpip"},
		ServiceStartName: "LocalSystem",
		DisplayName:      windowsServiceDisplayName,
		Description:      windowsServiceDescription,
	}
	service, err := manager.OpenService(name)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		arguments, parseErr := windows.DecomposeCommandLine(binaryPathName)
		if parseErr != nil || len(arguments) == 0 {
			return E.Cause(parseErr, "parse the service command line")
		}
		// CreateService composes the command line itself; UpdateConfig
		// below writes the exact one (ComposeCommandLine).
		service, err = manager.CreateService(name, arguments[0], config, arguments[1:]...)
		if errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
			return E.New("service ", name, " is marked for deletion: close the Services console (services.msc) and any sc.exe query, or reboot, then install again")
		}
		if err != nil {
			return E.Cause(err, "create service ", name)
		}
	} else if err != nil {
		return E.Cause(err, "open service ", name)
	}
	defer service.Close()
	if err = service.UpdateConfig(config); err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
			return E.New("service ", name, " is marked for deletion: close the Services console (services.msc), or reboot, then install again")
		}
		return E.Cause(err, "configure service ", name)
	}
	restart := mgr.RecoveryAction{Type: mgr.ServiceRestart, Delay: 5 * time.Second}
	if err = service.SetRecoveryActions([]mgr.RecoveryAction{restart, restart, restart}, 86400); err != nil {
		return E.Cause(err, "set the recovery actions of ", name)
	}
	// Without it a stop with a non-zero exit code (a failed Run, §2.10) is
	// not a failure to the SCM and nothing restarts.
	if err = service.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return E.Cause(err, "set the recovery flag of ", name)
	}
	descriptor, err := windows.SecurityDescriptorFromString(windowsServiceSDDL)
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if err = windows.SetSecurityInfo(service.Handle, windows.SE_SERVICE,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return E.Cause(err, "protect service ", name)
	}
	return nil
}

func (scm systemSCM) remove(out io.Writer, name string) error {
	manager, service, err := scm.open(name)
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	defer service.Close()
	if err = stopAndWait(out, service); err != nil {
		return err
	}
	if err = service.Delete(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
		return E.Cause(err, "delete service ", name)
	}
	return nil
}
