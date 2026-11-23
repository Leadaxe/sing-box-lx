//go:build with_lxd

package lxd

import (
	"bytes"
	"path/filepath"
	"strings"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/common/x/list"
)

// Replacing a rule-set a config still points at (SPEC 159).
//
// The previous bytes are kept beside the resource until a core has proven the
// new ones: either the running core reloaded them, or a core started with
// them on disk. Until then every path that could strand a config on a set it
// cannot load puts the previous bytes back:
//
//   - the running core does not take the new set in time → restored at once,
//     the PUT fails;
//   - nobody is there to confirm (core stopped, or the running config does not
//     load the file) → the copy waits; the first start that succeeds drops it,
//     the first start that fails restores it before the daemon falls back to
//     last-good.

// defaultRuleSetConfirmTimeout bounds the wait for the running core to reload
// a replaced rule-set. The core reacts within fswatch's 100 ms settle time
// plus the load itself; the margin is for a large set on a slow router.
const defaultRuleSetConfirmTimeout = 10 * time.Second

// liveRuleSet is the slice of adapter.RuleSet the confirmation needs.
type liveRuleSet interface {
	RegisterCallback(callback adapter.RuleSetUpdateCallback) *list.Element[adapter.RuleSetUpdateCallback]
	UnregisterCallback(element *list.Element[adapter.RuleSetUpdateCallback])
}

// ruleSetSource is the optional view of the running core's rule-sets. The
// controller type-asserts for it, as it does for statsSource: a service that
// cannot answer simply leaves every replacement unconfirmed.
type ruleSetSource interface {
	LiveRuleSet(tag string) (liveRuleSet, bool)
}

// Outcomes reported to the client in the PUT response.
const (
	replaceConfirmed = "confirmed" // the running core loaded the new set
	replacePending   = "pending"   // previous copy kept until a core starts
)

// localRuleSetTags returns the tags of the `type: local` rule-sets in config
// content whose path is one of the given files.
func localRuleSetTags(content string, paths ...string) []string {
	var config struct {
		Route struct {
			RuleSet []struct {
				Type string                     `json:"type"`
				Tag  badoption.Listable[string] `json:"tag"`
				Path string                     `json:"path"`
			} `json:"rule_set"`
		} `json:"route"`
	}
	if err := json.Unmarshal([]byte(content), &config); err != nil {
		return nil
	}
	var tags []string
	for _, ruleSet := range config.Route.RuleSet {
		if ruleSet.Type != C.RuleSetTypeLocal {
			continue
		}
		// One entry may declare several tags; each is its own rule-set, with
		// `{tag}` in the path expanded per tag.
		for _, tag := range ruleSet.Tag {
			path := filepath.Clean(strings.ReplaceAll(ruleSet.Path, C.RuleSetTagPlaceholder, tag))
			for _, candidate := range paths {
				if path == candidate {
					tags = append(tags, tag)
					break
				}
			}
		}
	}
	return tags
}

// liveRuleSets returns the running core's rule-sets that load the resource
// `name`; nil when no core is up or its config does not load the file.
func (c *controller) liveRuleSets(name string) []liveRuleSet {
	source, ok := c.service.(ruleSetSource)
	if !ok {
		return nil
	}
	c.stateAccess.Lock()
	running, active := c.running, c.activeContent
	c.stateAccess.Unlock()
	if !running {
		return nil
	}
	paths := []string{filepath.Clean(c.resources.pathOf(name))}
	if absolute, err := filepath.Abs(paths[0]); err == nil && absolute != paths[0] {
		paths = append(paths, absolute)
	}
	if c.infoResourcesDir != "" {
		paths = append(paths, filepath.Join(c.infoResourcesDir, name))
	}
	var sets []liveRuleSet
	for _, tag := range localRuleSetTags(active, paths...) {
		if ruleSet, loaded := source.LiveRuleSet(tag); loaded {
			sets = append(sets, ruleSet)
		}
	}
	return sets
}

// replaceReferenced swaps a referenced rule-set whose content already passed
// checkRuleSetContent. Caller holds applyAccess. A non-nil rejected is the
// running core's refusal (the previous bytes are back in place); err is disk.
func (c *controller) replaceReferenced(name string, content []byte) (meta resourceMeta, outcome string, rejected error, err error) {
	meta = resourceMeta{Name: name, SHA256: contentSHA(string(content)), Size: len(content)}
	current, found, err := c.resources.Read(name)
	if err != nil {
		return meta, "", nil, err
	}
	if found && bytes.Equal(current, content) {
		// Nothing to prove: rewriting the same bytes would only make the core
		// reload them. A copy kept by an earlier upload stays as it is.
		return meta, "", nil, nil
	}

	sets := c.liveRuleSets(name)
	reloaded := make(chan struct{}, len(sets))
	for _, ruleSet := range sets {
		element := ruleSet.RegisterCallback(func(adapter.RuleSet) {
			select {
			case reloaded <- struct{}{}:
			default:
			}
		})
		defer ruleSet.UnregisterCallback(element)
	}

	_, _, kept, err := c.resources.PutKeepingPrevious(name, content)
	if err != nil {
		return meta, "", nil, err
	}
	if len(sets) == 0 {
		if !kept {
			return meta, "", nil, nil
		}
		return meta, replacePending, nil, nil
	}

	timeout := c.ruleSetConfirmTimeout
	if timeout <= 0 {
		timeout = defaultRuleSetConfirmTimeout
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for range sets {
		select {
		case <-reloaded:
		case <-deadline.C:
			if _, restoreErr := c.resources.RestorePrevious(name); restoreErr != nil {
				return meta, "", nil, E.Cause(restoreErr, "restore the previous rule-set")
			}
			return meta, "", E.New("the running core did not load the rule-set within ", timeout, " (its reason is in the core log); the previous version is back in place"), nil
		}
	}
	if err = c.resources.DropPrevious(name); err != nil {
		log.Warn(E.Cause(err, "lxd: drop the previous copy of resource ", name))
	}
	return meta, replaceConfirmed, nil, nil
}

// confirmReplacedResources runs after a core start that succeeded: whatever
// is on disk has just been loaded, so the kept copies are no longer needed.
func (c *controller) confirmReplacedResources() {
	if c.resources == nil {
		return
	}
	dropped, err := c.resources.DropAllPrevious()
	if err != nil {
		log.Warn(E.Cause(err, "lxd: drop previous resource copies"))
	}
	if len(dropped) > 0 {
		log.Info("lxd: replaced resources confirmed by the core start: ", strings.Join(dropped, ", "))
	}
}

// revertReplacedResources runs after a core start that failed: unconfirmed
// replacements are undone so the next attempt (the fall back to last-good, a
// retry) loads the bytes a core is known to have run with. Returns how many
// resources were put back.
func (c *controller) revertReplacedResources() int {
	if c.resources == nil {
		return 0
	}
	restored, err := c.resources.RestoreAllPrevious()
	if err != nil {
		log.Warn(E.Cause(err, "lxd: restore previous resource copies"))
	}
	if len(restored) > 0 {
		log.Warn("lxd: core start failed with unconfirmed resources, previous versions restored: ", strings.Join(restored, ", "))
	}
	return len(restored)
}
