//go:build with_lxd

package lxd

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"

	"github.com/sagernet/sing-box/common/srs"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/route/rule"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
)

// checkRuleSetContent loads content the way the core loads a local rule-set
// (route/rule.LocalRuleSet.reloadFile): decode, upgrade, build every rule. The
// format comes from the name's extension, as the core picks it for a config
// without an explicit `format`.
//
// It is the gate for replacing a resource a config still points at (SPEC 159):
// bytes the core could not load never reach the disk. checkable=false means
// the name is not a rule-set this daemon can vouch for (a geo database, an
// unknown extension).
//
// What it cannot answer is whether a particular config accepts the set (the
// DNS router refuses some rule-sets for some rules); that is settled by the
// core itself, see replaceReferenced.
func checkRuleSetContent(name string, content []byte) (checkable bool, err error) {
	var ruleSet option.PlainRuleSetCompat
	switch strings.ToLower(filepath.Ext(name)) {
	case ".srs":
		ruleSet, err = srs.Read(bytes.NewReader(content), false)
	case ".json":
		ruleSet, err = json.UnmarshalExtended[option.PlainRuleSetCompat](content)
	default:
		return false, nil
	}
	if err != nil {
		return true, E.Cause(err, "decode rule-set")
	}
	plainRuleSet, err := ruleSet.Upgrade()
	if err != nil {
		return true, E.Cause(err, "decode rule-set")
	}
	for index, ruleOptions := range plainRuleSet.Rules {
		if _, err = rule.NewHeadlessRule(context.Background(), ruleOptions); err != nil {
			return true, E.Cause(err, "build rule-set rules.[", index, "]")
		}
	}
	return true, nil
}
