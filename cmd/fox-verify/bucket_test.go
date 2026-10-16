// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thefoxbyte/foxbyte/internal/ledger"
)

func TestS3Config(t *testing.T) {
	t.Setenv("FOX_VERIFY_S3_SECRET_KEY", "s")
	c, prefix, err := s3Config("s3://acme-anchors/fox/anchors/main/", "", "eu-west-1", "AK", false)
	if err != nil || c.Bucket != "acme-anchors" || prefix != "fox/anchors/main/" || c.Endpoint != "https://s3.eu-west-1.amazonaws.com" {
		t.Errorf("%+v %q %v", c, prefix, err)
	}
	for _, bad := range []string{"acme/anchors/main", "s3://acme", "s3:///anchors/main"} {
		if _, _, err := s3Config(bad, "", "us-east-1", "AK", false); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	t.Setenv("FOX_VERIFY_S3_SECRET_KEY", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	if _, _, err := s3Config("s3://acme/anchors/main", "", "us-east-1", "AK", false); err == nil {
		t.Error("no secret was accepted")
	}
}

// The whole path against a store that answers like S3: two anchors, one hidden
// by a delete marker, the other rewritten by a second version.
func TestReadBucket(t *testing.T) {
	a1 := ledger.Anchor{Format: ledger.AnchorFormat, CheckpointID: 1, FromID: 1, ToID: 5, MerkleRoot: "r1"}
	a2 := ledger.Anchor{Format: ledger.AnchorFormat, CheckpointID: 2, FromID: 6, ToID: 9, MerkleRoot: "r2", PrevRoot: "r1"}
	forged := a2
	forged.MerkleRoot = "forged"
	k1, k2 := "anchors/main/"+ledger.AnchorFileName(5), "anchors/main/"+ledger.AnchorFileName(9)
	bodies := map[string]ledger.Anchor{k1 + "@v1": a1, k2 + "@v2": a2, k2 + "@v3": forged}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		q := r.URL.Query()
		key := strings.TrimPrefix(r.URL.Path, "/b/")
		switch {
		case q.Has("versions"):
			fmt.Fprintf(w, `<ListVersionsResult><IsTruncated>false</IsTruncated>
<Version><Key>%[1]s</Key><VersionId>v1</VersionId><LastModified>2026-10-16T10:00:00Z</LastModified></Version>
<DeleteMarker><Key>%[1]s</Key><VersionId>dm</VersionId><IsLatest>true</IsLatest><LastModified>2026-10-16T11:00:00Z</LastModified></DeleteMarker>
<Version><Key>%[2]s</Key><VersionId>v2</VersionId><LastModified>2026-10-16T12:00:00Z</LastModified></Version>
<Version><Key>%[2]s</Key><VersionId>v3</VersionId><IsLatest>true</IsLatest><LastModified>2026-10-16T13:00:00Z</LastModified></Version>
</ListVersionsResult>`, k1, k2)
		case q.Has("retention"):
			fmt.Fprint(w, `<Retention><Mode>COMPLIANCE</Mode><RetainUntilDate>2027-10-16T00:00:00Z</RetainUntilDate></Retention>`)
		default:
			a, ok := bodies[key+"@"+q.Get("versionId")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(a)
		}
	}))
	defer srv.Close()
	t.Setenv("FOX_VERIFY_S3_SECRET_KEY", "s")
	cfg, prefix, err := s3Config("s3://b/anchors/main", srv.URL, "us-east-1", "AK", true)
	if err != nil {
		t.Fatal(err)
	}
	res, err := readBucket(cfg, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if res.versions != 4 || len(res.anchors) != 2 || res.anchors[1].MerkleRoot != "r2" {
		t.Fatalf("%+v", res)
	}
	joined := strings.Join(res.problems, "\n")
	for _, want := range []string{"was deleted from the bucket", "was overwritten in the bucket"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	if strings.Contains(strings.Join(res.notes, "\n"), "COMPLIANCE") {
		t.Errorf("all locked in COMPLIANCE, yet: %v", res.notes)
	}
}
