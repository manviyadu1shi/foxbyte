// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"strings"
	"testing"
	"time"
)

// The report is the deliverable, so what it says is a test rather than a hope.
// These are all pure: a DrillResult in, a document out.

func asyncResult() DrillResult {
	return DrillResult{
		At: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC), Writes: 400,
		Acked: 180, Survived: 169, LostRows: 11, WritesPerSecond: 22,
		TimeToService: 3400 * time.Millisecond,
		SyncMode:      false, ArchiveTimeout: archiveTimeoutSeconds,
		Target:       "s3://dr-bucket at https://minio.dr.example (region eu-west-1, key AKIA…)",
		TargetRemote: true, PGMajor: "18",
	}
}

// With asynchronous replication the measured loss is a sample, and the report
// must refuse to let it be quoted as a bound. This is the whole reason §5 of
// the plan says not to promise figures a drill has not measured — and the
// subtler half: a drill measures one workload, not a guarantee.
func TestAsyncReportWillNotLetTheNumberBeQuoted(t *testing.T) {
	got := asyncResult().Report()

	for _, want := range []string{
		"Acknowledged writes lost (RPO) | **11**",
		"0.5s", // 11 rows at 22/s, so the rows are given as time too
		"**Not the number above.**",
		"no bound on how far the standby",
		"a busier primary or a slower link would lose more",
		"at least every 60s",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report does not say %q\n%s", want, got)
		}
	}
	// And it names the way to turn the sample into a guarantee.
	if !strings.Contains(got, "ha sync on") {
		t.Error("the report does not name the setting that makes RPO zero")
	}
}

// With zero-data-loss mode on, zero is a property of the configuration. The
// report has to say that, because otherwise the next reader assumes it got
// lucky.
func TestSyncReportCallsZeroAProperty(t *testing.T) {
	r := asyncResult()
	r.SyncMode, r.LostRows, r.Survived = true, 0, r.Acked
	got := r.Report()

	for _, want := range []string{
		"**RPO zero**",
		"property of the configuration rather than of this sample",
		"confirmation, not the evidence",
		"zero-data-loss mode **on**",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report does not say %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "**Not the number above.**") {
		t.Error("the report hedges a number that is a guarantee")
	}
}

// The section that stops this being misused. A single-machine drill is not
// evidence of surviving a site, and the measured figures are the part that gets
// quoted — so the limits are in the document, not in somebody's memory.
func TestReportStatesWhatItDidNotTest(t *testing.T) {
	got := asyncResult().Report()
	for _, want := range []string{
		"What this drill did not test",
		"sharing its disk",
		"Detection and the decision to fail over were manual",
		"A second site",
		"phase 1", "phase 3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report does not say %q\n%s", want, got)
		}
	}
	// RTO is measured from the kill, and saying so matters: a number that
	// quietly excluded detection would read as a full recovery time.
	if !strings.Contains(got, "does not include noticing the failure") {
		t.Errorf("the report does not say what RTO excludes\n%s", got)
	}
}

// A local archive makes every figure in the report meaningless as a
// disaster-recovery number, so the report says so rather than leaving the
// reader to notice the target line.
func TestReportRefusesToBeDRFigureWhenTheArchiveIsLocal(t *testing.T) {
	r := asyncResult()
	r.TargetRemote = false
	r.Target = "the local object store (objstore, on this machine)"
	got := r.Report()
	for _, want := range []string{
		"**The archive is on this machine.**",
		"every backup of it at the same time",
		"backup target set",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report does not say %q\n%s", want, got)
		}
	}
	// And with a remote one it does not say any of that.
	if strings.Contains(asyncResult().Report(), "The archive is on this machine") {
		t.Error("the report warns about a local archive when the archive is remote")
	}
}

// Anything that happened which the numbers do not show has to reach the
// document, or a report is quietly incomplete.
func TestReportCarriesNotes(t *testing.T) {
	r := asyncResult()
	r.Notes = []string{"the writer stopped early: connection reset"}
	got := r.Report()
	if !strings.Contains(got, "## Notes") || !strings.Contains(got, "connection reset") {
		t.Errorf("a note did not reach the report\n%s", got)
	}
	if strings.Contains(asyncResult().Report(), "## Notes") {
		t.Error("an empty notes section was rendered")
	}
}

// The drill writes into a schema of its own, and the report is named for when
// the drill started rather than when it ended.
func TestDrillNamingIsItsOwn(t *testing.T) {
	if drillSchema == "public" || !strings.HasPrefix(drillSchema, "fox") {
		t.Errorf("the drill writes into %q, which is not obviously its own", drillSchema)
	}
	if !strings.Contains(asyncResult().Report(), "2026-10-13T09:00:00Z") {
		t.Error("the report is not dated")
	}
}

// The timeout the report quotes as the bound a buyer may be promised is the one
// the primary actually runs with. Two copies of that number could disagree, and
// the report would be the one nobody checked.
func TestTheQuotedBoundIsTheSettingInUse(t *testing.T) {
	realtimeOff(t)
	args := strings.Join(postgresArgs(true, PGMajor), " ")
	if !strings.Contains(args, "archive_timeout=60") {
		t.Errorf("the primary does not run with archive_timeout=60: %s", args)
	}
	if archiveTimeoutSeconds != 60 {
		t.Errorf("archiveTimeoutSeconds = %d, but the primary is started with 60", archiveTimeoutSeconds)
	}
}

// The most dangerous result this drill can produce, and the one a single-host
// install will usually produce: asynchronous replication that happened to lose
// nothing. It is the number somebody will want to put in a tender, and the
// configuration promises none of it.
func TestAsyncReportWithNothingLostIsTheDangerousOne(t *testing.T) {
	r := asyncResult()
	r.LostRows, r.Survived = 0, r.Acked
	got := r.Report()

	for _, want := range []string{
		"**Nothing, on the strength of this.**",
		"does not promise that",
		"a machine\nit shares with the primary",
		"nothing in this configuration bounds how many",
		"makes zero a guarantee rather than an observation",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report does not say %q\n%s", want, got)
		}
	}
	// It must not read like the guarantee that only sync mode gives.
	if strings.Contains(got, "**RPO zero**") {
		t.Error("an asynchronous drill that lost nothing is claiming RPO zero")
	}
	if strings.Contains(got, "property of the configuration") {
		t.Error("an asynchronous result is being called a property of the configuration")
	}
}

// Phase 6: a report must stop disclaiming what the drill actually did. The old
// one said "there is only one site" and "detection was manual" in every copy,
// which is as misleading as claiming a site it did not have — and the second
// kind of mistake is the only one anybody checks for.
func TestTheLimitsSectionIsTrueOfTheDrillThatRan(t *testing.T) {
	t.Run("one host, a person deciding: all three limits stand", func(t *testing.T) {
		r := asyncResult()
		r.Topology, r.Detection = TopologyOneHost, DetectionManual
		got := r.Report()
		for _, want := range []string{"sharing its disk", "A second site", "were manual", "phase 1", "phase 3"} {
			if !strings.Contains(got, want) {
				t.Errorf("the report does not say %q", want)
			}
		}
	})

	t.Run("two machines: it no longer claims one site", func(t *testing.T) {
		r := asyncResult()
		r.Topology, r.Detection = TopologyTwoMachines, DetectionManual
		got := r.Report()
		for _, gone := range []string{"A second site", "sharing its disk"} {
			if strings.Contains(got, gone) {
				t.Errorf("the report still says %q of a two-machine drill", gone)
			}
		}
		// And what is left is named, with the phase that closes it.
		if !strings.Contains(got, "were manual") || !strings.Contains(got, "ha watch") {
			t.Errorf("the report does not name automatic detection as what is left:\n%s", got)
		}
	})

	t.Run("a watcher deciding: it no longer claims detection was manual", func(t *testing.T) {
		r := asyncResult()
		r.Topology, r.Detection = TopologyOneHost, DetectionAutomatic
		got := r.Report()
		if strings.Contains(got, "were manual") {
			t.Error("the report says detection was manual of a drill the watcher decided")
		}
		if !strings.Contains(got, "node join") {
			t.Error("the report does not name surviving a machine as what is left")
		}
	})

	t.Run("both: nothing it set out to test is left, and that is said once", func(t *testing.T) {
		r := asyncResult()
		r.Topology, r.Detection = TopologyTwoMachines, DetectionAutomatic
		got := r.Report()
		if !strings.Contains(got, "Nothing it set out to") {
			t.Errorf("the report hedges a drill with nothing left to hedge:\n%s", got)
		}
		// The one thing no drill can test, which is a placement rather than a
		// mechanism, must still be said.
		if !strings.Contains(got, "whether the witness is where") {
			t.Error("the report does not name the part no drill can test")
		}
		for _, gone := range []string{"A second site", "were manual", "sharing its disk"} {
			if strings.Contains(got, gone) {
				t.Errorf("the report still disclaims %q", gone)
			}
		}
	})
}

// A takeover drill measures different things, and the one it cannot measure is
// the one somebody would most like a number for. A zero there would read as a
// guarantee of no loss, which a surviving machine cannot know.
func TestTakeoverReportRefusesToGuessRPO(t *testing.T) {
	r := DrillResult{
		At: asyncResult().At, Ran: true, Takeover: true,
		Topology: TopologyTwoMachines, Detection: DetectionAutomatic,
		Primary: "db1.example.com:5432", TimeToService: 42 * time.Second,
		LastReceived: "16/B374D848", ArchiveTimeout: archiveTimeoutSeconds,
		Target: "s3://dr-bucket", TargetRemote: true, PGMajor: "18",
	}
	got := r.Report()
	for _, want := range []string{
		"not measurable from this side",
		"db1.example.com:5432",
		"16/B374D848",
		"*includes* noticing",
		"ha watch`, with a witness agreeing",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the takeover report does not say %q\n%s", want, got)
		}
	}
	// It must not print the local drill's framing: nothing was killed here.
	for _, gone := range []string{"killed with SIGKILL", "Kill to a write", "Acknowledged writes lost (RPO) | **0"} {
		if strings.Contains(got, gone) {
			t.Errorf("the takeover report says %q", gone)
		}
	}
	// What may be quoted is the one number it has, and nothing about loss.
	for _, want := range []string{
		"the honest kind",
		"already contains the part a",
		"Nothing about data loss",
		"without anybody awake",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the takeover report does not say %q", want)
		}
	}
	// And none of the local drill's reasoning about rows, which had none here.
	for _, gone := range []string{"keeping up at", "Not the number above", "lost row(s) is what this workload"} {
		if strings.Contains(got, gone) {
			t.Errorf("the takeover report reasons about rows it never wrote: %q", gone)
		}
	}
	// A person deciding is reported as such, so the two cases are not confused
	// by whoever reads the number.
	r.Detection = DetectionManual
	m := r.Report()
	if !strings.Contains(m, "Who noticed | a person") {
		t.Error("a takeover a person noticed is not reported as such")
	}
	if !strings.Contains(m, "promises nothing about the next") {
		t.Error("a manually noticed takeover is quoted as if it were repeatable")
	}
}

// An unread WAL position must not print as a blank cell in a document somebody
// is reading to decide something.
func TestAnUnreadableLSNSaysSo(t *testing.T) {
	if got := lsnOrUnknown("  "); got != "could not be read" {
		t.Errorf("lsnOrUnknown(blank) = %q", got)
	}
	if got := lsnOrUnknown("16/B3"); got != "16/B3" {
		t.Errorf("lsnOrUnknown passed through as %q", got)
	}
}
