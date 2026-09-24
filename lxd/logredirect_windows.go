//go:build with_lxd && windows

package lxd

import (
	"context"
	"os"
	"time"

	"github.com/sagernet/sing-box/log"

	"golang.org/x/sys/windows"
)

const logRotationSupported = true

func init() {
	logRotateByCopy = true
}

// redirectStdIO makes one descriptor the process's stdout and stderr for its
// whole life (SPEC 103 §2.12): the standard handles point at it — the
// runtime reads them on every write, so panics and fatal errors land there —
// os.Stdout/os.Stderr become it (the core's logger is created over os.Stderr
// later, in box.New), and the package logger, made over the original
// os.Stderr at init, is replaced. Under the SCM the original handles are
// empty.
func redirectStdIO(file *os.File) error {
	handle := windows.Handle(file.Fd())
	if err := windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, handle); err != nil {
		return err
	}
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, handle); err != nil {
		return err
	}
	os.Stdout = file
	os.Stderr = file
	factory := log.NewDefaultFactory(context.Background(), log.Formatter{BaseTime: time.Now()}, file, "", nil, false)
	if err := factory.Start(); err != nil {
		return err
	}
	log.SetStdLogger(factory.Logger())
	return nil
}
