package option

import (
	"bytes"
	"strings"
	"sync"

	"github.com/sagernet/sing/common/json"
)

// lx: SPEC 165 — what the lx pre-pass changed in a node (ALGORITHM Н3).
//
// A change is one field: the dotted path to a leaf of the node's JSON object
// ("detour", "mtu", "tls.server_name"), the value before (absent when the
// field was absent) and after (absent when the change removes the field), and
// who made it. Applied in order to the node as written they give the node the
// box was built from (И4).

// The closed list of LXConfigChangeBy.Type.
const (
	LXChangeByChain        = "chain"
	LXChangeByChainRewrite = "chain_rewrite"
	// LXChangeByDetour: a field upstream does not apply under detour
	// (SPEC 165 Д1).
	LXChangeByDetour   = "lx.detour"
	LXChangeByMTUAlign = "mtu_align"
)

type LXConfigChange struct {
	Field string           `json:"field"`
	From  any              `json:"from,omitempty"`
	To    any              `json:"to,omitempty"`
	By    LXConfigChangeBy `json:"by"`
}

// LXConfigChangeBy says who made a change; the fields after Type depend on
// it: chain and position for chain and chain_rewrite, node_type for
// chain_rewrite, mode, below, overhead and reason for mtu_align.
type LXConfigChangeBy struct {
	Type     string `json:"type"`
	Chain    string `json:"chain,omitempty"`
	Position int    `json:"position,omitempty"`
	NodeType string `json:"node_type,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Below    string `json:"below,omitempty"`
	Overhead int    `json:"overhead,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// ApplyLXConfigChanges applies changes in order to node, a node's JSON object
// as decoded into map[string]any, in place.
func ApplyLXConfigChanges(node map[string]any, changes []LXConfigChange) {
	for _, change := range changes {
		path := strings.Split(change.Field, ".")
		parent := node
		for _, key := range path[:len(path)-1] {
			next, isObject := parent[key].(map[string]any)
			if !isObject {
				if change.To == nil {
					parent = nil
					break
				}
				next = make(map[string]any)
				parent[key] = next
			}
			parent = next
		}
		if parent == nil {
			continue
		}
		last := path[len(path)-1]
		if change.To == nil {
			delete(parent, last)
		} else {
			parent[last] = change.To
		}
	}
}

// LXEffectiveConfig is the config a box was built from after the lx pre-pass
// (lx: SPEC 165 Н2), with the changes it made per node tag. The box registers
// it in its service registry; it is rendered on first use, in the canonical
// form of the running config (SPEC 037), so that original and effective
// differ by the changes alone.
type LXEffectiveConfig struct {
	options Options
	changes map[string][]LXConfigChange

	once    sync.Once
	content string
	err     error
}

func NewLXEffectiveConfig(options Options, changes map[string][]LXConfigChange) *LXEffectiveConfig {
	return &LXEffectiveConfig{options: options, changes: changes}
}

// Content returns the canonical indented JSON of the effective config.
func (c *LXEffectiveConfig) Content() (string, error) {
	c.once.Do(func() {
		var buffer bytes.Buffer
		encoder := json.NewEncoder(&buffer)
		encoder.SetIndent("", "  ")
		c.err = encoder.Encode(c.options)
		c.content = buffer.String()
	})
	return c.content, c.err
}

// Changes returns the changes of the node tagged tag, in the order they were
// made (П2 → П5 → М4).
func (c *LXEffectiveConfig) Changes(tag string) []LXConfigChange {
	return c.changes[tag]
}
