package option

import (
	"github.com/sagernet/sing/common/byteformats"
	"github.com/sagernet/sing/common/json/badoption"
)

type OOMKillerServiceOptions struct {
	MemoryLimit         *byteformats.MemoryBytes `json:"memory_limit,omitempty"`
	SafetyMargin        *byteformats.MemoryBytes `json:"safety_margin,omitempty"`
	MinInterval         badoption.Duration       `json:"min_interval,omitempty"`
	MaxInterval         badoption.Duration       `json:"max_interval,omitempty"`
	KillerDisabled      bool                     `json:"-"`
	MemoryLimitOverride uint64                   `json:"-"`
	MemoryLimitHeap     bool                     `json:"-"` // lx: SPEC 148 — compare the limit with the Go runtime memory instead of the process RSS
}
