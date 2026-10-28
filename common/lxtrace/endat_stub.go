//go:build !with_lx_command

package lxtrace

import "time"

func (s *Stage) EndAt(end time.Time, err error) {}
