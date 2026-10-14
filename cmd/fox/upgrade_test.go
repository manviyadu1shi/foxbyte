// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"testing"

	"github.com/thefoxbyte/foxbyte/internal/update"
)

func TestParseUpgradeArgs(t *testing.T) {
	o, err := parseUpgradeArgs([]string{"--edition", "Enterprise", "--accept-license", "-y"})
	if err != nil || o.Edition != update.EditionEnterprise || !o.AcceptLicense || !o.Yes {
		t.Fatalf("got %+v, %v", o, err)
	}
	if o, err := parseUpgradeArgs([]string{"--edition=standard"}); err != nil || o.Edition != update.EditionStandard || o.AcceptLicense {
		t.Fatalf("--edition=standard: %+v, %v", o, err)
	}
	for _, bad := range [][]string{{}, {"--edition"}, {"--edition", "gold"}, {"--edition", "enterprise", "--version", "v1"}} {
		if _, err := parseUpgradeArgs(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
