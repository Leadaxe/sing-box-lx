//go:build !with_lx_command

package lxtrace

import "testing"

// Without with_lx_command there are no findings.
func TestStubFindingsEmpty(t *testing.T) {
	summary := Summary{Stages: []StageSnapshot{{ID: 1, Kind: "reality", Attrs: []Attr{{Key: "verdict", Value: "dest_cert"}}}}}
	if findings := Findings(summary, FindingState{PackageName: "x", TUN: []TUNState{{ExcludePackage: []string{"x"}}}}); findings != nil {
		t.Fatalf("stub findings = %+v", findings)
	}
}
