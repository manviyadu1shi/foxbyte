// SPDX-License-Identifier: AGPL-3.0-or-later

package ledger

import (
	"strings"
	"testing"
)

// The data-change triggers are refused being switched off in any way an
// ordinary session would notice, not only by DISABLE. A trigger made to fire
// only in replica mode (tgenabled 'R') never fires in an ordinary session, and
// a table's owner may do that without being a superuser; it used to pass. The
// behaviour is proved in integration-v2 §10; this guards the check itself.
func TestGuardTriggersCannotBeMadeReplicaOnly(t *testing.T) {
	if !strings.Contains(SchemaData, "t.tgenabled IN ('D','R')") {
		t.Error("the trigger check no longer refuses both disabled and replica-only triggers")
	}
	// The refusal keeps the wording scripts and people already match on.
	if !strings.Contains(SchemaData, "cannot be disabled or renamed, nor made replica-only") {
		t.Error("the refusal's message changed")
	}
}
