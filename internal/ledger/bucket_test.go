// SPDX-License-Identifier: AGPL-3.0-or-later

package ledger

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)

func anchorJSON(t *testing.T, a Anchor) []byte {
	t.Helper()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func hasLine(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// What a bucket's versions say about the anchors in it.
func TestBucketAnchors(t *testing.T) {
	a1 := Anchor{Format: AnchorFormat, CheckpointID: 1, FromID: 1, ToID: 5, MerkleRoot: "r1"}
	a2 := Anchor{Format: AnchorFormat, CheckpointID: 2, FromID: 6, ToID: 9, MerkleRoot: "r2", PrevRoot: "r1"}
	forged := a2
	forged.MerkleRoot = "forged"
	k1, k2 := "anchors/main/"+AnchorFileName(5), "anchors/main/"+AnchorFileName(9)
	objs := []BucketObject{
		{Key: k1, VersionID: "1", LastModified: t0, Body: anchorJSON(t, a1), Retention: "COMPLIANCE", RetentionKnown: true},
		{Key: k1, VersionID: "dm", LastModified: t0.Add(time.Hour), DeleteMarker: true},
		{Key: k2, VersionID: "2", LastModified: t0.Add(2 * time.Hour), Body: anchorJSON(t, a2), Retention: "GOVERNANCE", RetentionKnown: true},
		{Key: k2, VersionID: "3", LastModified: t0.Add(3 * time.Hour), Body: anchorJSON(t, forged)},
		{Key: "anchors/main/" + AnchorFileName(9) + ".mirrored", VersionID: "x", LastModified: t0},
	}
	anchors, problems, notes := BucketAnchors(objs)
	if len(anchors) != 2 || anchors[1].MerkleRoot != "r2" {
		t.Fatalf("the first version of each is the anchor of record: %+v", anchors)
	}
	if !hasLine(problems, "was deleted from the bucket") || !hasLine(problems, "its locked version is still there") {
		t.Errorf("a delete marker is a finding: %v", problems)
	}
	if !hasLine(problems, "was overwritten in the bucket") {
		t.Errorf("a different second version is a finding: %v", problems)
	}
	if !hasLine(notes, "not locked in COMPLIANCE mode") {
		t.Errorf("GOVERNANCE is noted: %v", notes)
	}

	// An identical second copy is not a rewrite, and an unreadable retention
	// is said rather than assumed.
	_, problems, notes = BucketAnchors([]BucketObject{
		{Key: k1, LastModified: t0, Body: anchorJSON(t, a1)},
		{Key: k1, LastModified: t0.Add(time.Minute), Body: append(anchorJSON(t, a1), '\n')},
	})
	if len(problems) != 0 || !hasLine(notes, "more than once, identically") || !hasLine(notes, "could not be read") {
		t.Errorf("identical copies: %v / %v", problems, notes)
	}

	// Only a delete marker left, or something that is not an anchor.
	_, problems, _ = BucketAnchors([]BucketObject{
		{Key: k1, LastModified: t0, DeleteMarker: true},
		{Key: k2, LastModified: t0, Body: []byte("not json")},
	})
	if !hasLine(problems, "only a delete marker") || !hasLine(problems, "is not an anchor") {
		t.Errorf("%v", problems)
	}
}

// Gaps at either end: anchors missing from the start, and anchoring that has
// stopped while rows kept being written.
func TestCoverage(t *testing.T) {
	rows := []Row{{ID: 1, At: t0}, {ID: 5, At: t0}, {ID: 6, At: t0.Add(time.Hour)}, {ID: 7, At: t0.Add(2 * time.Hour)}}
	whole := []Anchor{{CheckpointID: 1, FromID: 1, ToID: 5, CreatedAt: t0}}
	if p, _ := Coverage(rows, whole, t0.Add(90*time.Minute), 24*time.Hour); len(p) != 0 {
		t.Errorf("rows waiting less than maxAge are not a finding: %v", p)
	}
	p, _ := Coverage(rows, whole, t0.Add(30*time.Hour), 24*time.Hour)
	if !hasLine(p, "anchoring has stopped: 2 ledger row(s)") || !hasLine(p, "checkpoint 1") {
		t.Errorf("stale tail: %v", p)
	}
	// A quiet ledger is not a stopped one.
	if p, _ := Coverage(rows[:2], whole, t0.Add(1000*time.Hour), 24*time.Hour); len(p) != 0 {
		t.Errorf("nothing written since the last anchor: %v", p)
	}
	headless := []Anchor{{Branch: "main", CheckpointID: 2, FromID: 6, ToID: 7, PrevRoot: "r1", CreatedAt: t0}}
	if p, _ := Coverage(rows, headless, t0, 24*time.Hour); !hasLine(p, "no anchor before checkpoint 2") {
		t.Errorf("main with its start missing: %v", p)
	}
	// A branch's first anchor continues its parent's: not a gap, but said.
	forked := []Anchor{{Branch: "qa", CheckpointID: 2, FromID: 6, ToID: 7, PrevRoot: "r1", CreatedAt: t0}}
	if p, n := Coverage(rows, forked, t0, 24*time.Hour); len(p) != 0 || !hasLine(n, "anchored under the parent") {
		t.Errorf("a branch: %v / %v", p, n)
	}
	if p, _ := Coverage(rows, nil, t0.Add(48*time.Hour), 24*time.Hour); !hasLine(p, "no anchors in the bucket") {
		t.Errorf("no anchors at all: %v", p)
	}
}

// The local directory against the locked copies.
func TestCompareAnchors(t *testing.T) {
	a := Anchor{Format: AnchorFormat, CheckpointID: 1, FromID: 1, ToID: 5, MerkleRoot: "r1", CreatedAt: t0}
	b := Anchor{Format: AnchorFormat, CheckpointID: 2, FromID: 6, ToID: 9, MerkleRoot: "r2", CreatedAt: t0}
	c := Anchor{Format: AnchorFormat, CheckpointID: 3, FromID: 10, ToID: 12, MerkleRoot: "r3", CreatedAt: t0.Add(47 * time.Hour)}
	rewritten := a
	rewritten.MerkleRoot = "changed"

	p, n := CompareAnchors([]Anchor{a, b}, []Anchor{a, b}, t0, time.Hour)
	if len(p)+len(n) != 0 {
		t.Errorf("identical: %v %v", p, n)
	}
	p, _ = CompareAnchors([]Anchor{rewritten, b}, []Anchor{a, b}, t0, time.Hour)
	if !hasLine(p, "differs from its locked copy") {
		t.Errorf("rewritten locally: %v", p)
	}
	p, _ = CompareAnchors([]Anchor{b}, []Anchor{a, b}, t0, time.Hour)
	if !hasLine(p, "missing here") {
		t.Errorf("removed locally: %v", p)
	}
	p, n = CompareAnchors([]Anchor{a, b, c}, []Anchor{a}, t0.Add(48*time.Hour), 24*time.Hour)
	if !hasLine(p, AnchorFileName(9)+" here was never copied") || !hasLine(n, AnchorFileName(12)+" is not in the bucket yet") {
		t.Errorf("not copied: %v / %v", p, n)
	}
}
