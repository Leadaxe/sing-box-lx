//go:build with_lxd && !windows

package lxd

import (
	"context"

	E "github.com/sagernet/sing/common/exceptions"
)

// ServiceBody is the daemon as a service handler runs it.
type ServiceBody func(ctx context.Context) error

// IsWindowsService: there is no SCM here.
func IsWindowsService() bool {
	return false
}

// RunService is only reached under the Windows SCM.
func RunService(prepare func() (ServiceBody, error)) error {
	return E.New("not running under the Windows service manager")
}
