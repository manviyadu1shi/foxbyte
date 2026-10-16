// SPDX-License-Identifier: AGPL-3.0-or-later

package ledger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

// Checking anchors kept in a bucket under Object Lock (the expert review plan,
// G3). The bucket keeps every version of every object, and a locked version
// cannot be removed before its retention ends. So what someone did to the
// anchors there is visible in the versions themselves:
//
//   - a delete marker over an anchor is an attempt to hide it; the locked
//     version underneath is still there, and is what gets checked;
//   - a second version of an anchor is an attempt to rewrite it; the first
//     version is the anchor of record;
//   - an anchor missing from the start of the sequence, or anchors that stop
//     while the ledger keeps growing, is a gap, and a gap is what someone
//     rewriting history would want.
//
// These functions decide; reading the bucket is cmd/fox-verify's.

// BucketObject is one version of an object in an anchor bucket, as read.
type BucketObject struct {
	Key          string
	VersionID    string
	LastModified time.Time
	DeleteMarker bool
	Body         []byte // nil for a delete marker
	// Retention is the version's Object Lock mode ("" when it has none), when
	// RetentionKnown; the keys may not be allowed to read it.
	Retention      string
	RetentionKnown bool
}

// BucketAnchors turns a bucket's versions into the anchors of record, one per
// key, from each key's first version, and reports what the versions show.
func BucketAnchors(objs []BucketObject) (anchors []Anchor, problems, notes []string) {
	byKey := map[string][]BucketObject{}
	for _, o := range objs {
		if strings.HasSuffix(o.Key, ".json") {
			byKey[o.Key] = append(byKey[o.Key], o)
		}
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	notCompliance, retentionUnknown := 0, 0
	for _, k := range keys {
		vs := byKey[k]
		sort.Slice(vs, func(i, j int) bool { return vs[i].LastModified.Before(vs[j].LastModified) })
		name := path.Base(k)
		var data []BucketObject
		for _, v := range vs {
			if v.DeleteMarker {
				problems = append(problems, fmt.Sprintf("anchor %s was deleted from the bucket at %s (a delete marker hides it); its locked version is still there and was checked",
					name, v.LastModified.UTC().Format(time.RFC3339)))
				continue
			}
			data = append(data, v)
		}
		if len(data) == 0 {
			problems = append(problems, fmt.Sprintf("anchor %s has only a delete marker in the bucket: no version of it is left to check", name))
			continue
		}
		first := data[0]
		for _, later := range data[1:] {
			if bytes.Equal(bytes.TrimSpace(later.Body), bytes.TrimSpace(first.Body)) {
				notes = append(notes, fmt.Sprintf("anchor %s was copied to the bucket more than once, identically", name))
				continue
			}
			problems = append(problems, fmt.Sprintf("anchor %s was overwritten in the bucket at %s; the first version, kept by the bucket, is what was checked",
				name, later.LastModified.UTC().Format(time.RFC3339)))
		}
		var a Anchor
		if err := json.Unmarshal(first.Body, &a); err != nil {
			problems = append(problems, fmt.Sprintf("anchor %s in the bucket is not an anchor: %v", name, err))
			continue
		}
		anchors = append(anchors, a)
		switch {
		case !first.RetentionKnown:
			retentionUnknown++
		case first.Retention != "COMPLIANCE":
			notCompliance++
		}
	}
	if notCompliance > 0 {
		notes = append(notes, fmt.Sprintf("%d anchor(s) in the bucket are not locked in COMPLIANCE mode, so someone allowed to bypass their retention could remove them", notCompliance))
	}
	if retentionUnknown > 0 {
		notes = append(notes, fmt.Sprintf("the retention of %d anchor(s) could not be read with these keys (s3:GetObjectRetention)", retentionUnknown))
	}
	return anchors, problems, notes
}

// Coverage reports the gaps at either end of a set of anchors: the earliest
// one continuing an anchor that is not there, and rows written since the last
// anchor for longer than maxAge. A gap in the middle is already one in Verify.
//
// A missing start is a problem only for main. Every other branch is a clone,
// and its first anchor continues its parent's last checkpoint: the rows before
// it are anchored under the parent, so it is said, with how to check them.
func Coverage(rows []Row, anchors []Anchor, now time.Time, maxAge time.Duration) (problems, notes []string) {
	anchors = append([]Anchor(nil), anchors...)
	sort.Slice(anchors, func(i, j int) bool { return anchors[i].ToID < anchors[j].ToID })
	var lastTo int64
	if len(anchors) > 0 {
		first := anchors[0]
		if first.PrevRoot != "" {
			if first.Branch == "" || first.Branch == "main" {
				problems = append(problems, fmt.Sprintf("the bucket has no anchor before checkpoint %d (ledger ids %d–%d): earlier anchors are missing, "+
					"or were removed after their retention ended", first.CheckpointID, first.FromID, first.ToID))
			} else {
				notes = append(notes, fmt.Sprintf("branch %s's first anchor continues its parent's: rows before id %d are anchored under the parent "+
					"(check them with --anchors-s3 …/anchors/<parent>)", first.Branch, first.FromID))
			}
		}
		lastTo = anchors[len(anchors)-1].ToID
	}
	var waiting int
	var oldest time.Time
	for _, r := range rows {
		if r.ID <= lastTo {
			continue
		}
		waiting++
		if oldest.IsZero() || r.At.Before(oldest) {
			oldest = r.At
		}
	}
	if waiting > 0 && now.Sub(oldest) > maxAge {
		since := "there are no anchors in the bucket"
		if len(anchors) > 0 {
			l := anchors[len(anchors)-1]
			since = fmt.Sprintf("the last anchor is checkpoint %d, made %s", l.CheckpointID, l.CreatedAt.UTC().Format(time.RFC3339))
		}
		problems = append(problems, fmt.Sprintf("anchoring has stopped: %d ledger row(s) are not anchored in the bucket, the oldest written %s (more than %s ago); %s",
			waiting, oldest.UTC().Format(time.RFC3339), maxAge, since))
	}
	return problems, notes
}

// CompareAnchors checks a local anchor directory against the bucket's anchors
// of record: a local anchor that differs from its locked copy was rewritten,
// one the bucket lacks was never copied (a finding once it is older than
// maxAge), and one the bucket has but this directory lacks was removed here.
func CompareAnchors(local, bucket []Anchor, now time.Time, maxAge time.Duration) (problems, notes []string) {
	inBucket := map[int64]Anchor{}
	for _, a := range bucket {
		inBucket[a.ToID] = a
	}
	seen := map[int64]bool{}
	for _, l := range local {
		seen[l.ToID] = true
		b, ok := inBucket[l.ToID]
		name := AnchorFileName(l.ToID)
		switch {
		case !ok && now.Sub(l.CreatedAt) > maxAge:
			problems = append(problems, fmt.Sprintf("anchor %s here was never copied to the bucket (made %s)", name, l.CreatedAt.UTC().Format(time.RFC3339)))
		case !ok:
			notes = append(notes, fmt.Sprintf("anchor %s is not in the bucket yet", name))
		case !sameAnchor(l, b):
			problems = append(problems, fmt.Sprintf("anchor %s here differs from its locked copy in the bucket: the local file was rewritten", name))
		}
	}
	for _, b := range bucket {
		if !seen[b.ToID] {
			problems = append(problems, fmt.Sprintf("anchor %s is in the bucket but missing here: the local file was removed", AnchorFileName(b.ToID)))
		}
	}
	return problems, notes
}

func sameAnchor(a, b Anchor) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
