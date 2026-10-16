// SPDX-License-Identifier: AGPL-3.0-or-later

package s3read

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// AWS's published Signature Version 4 examples for S3 (the "Examples:
// Signature Calculations" page): the same keys, time and request must give the
// same signature, or no real store will accept these requests.
var awsExample = Config{
	Endpoint:  "https://s3.amazonaws.com",
	Region:    "us-east-1",
	Bucket:    "examplebucket",
	AccessKey: "AKIAIOSFODNN7EXAMPLE",
	SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	now:       func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) },
}

func signature(t *testing.T, req *http.Request) string {
	t.Helper()
	a := req.Header.Get("Authorization")
	i := strings.Index(a, "Signature=")
	if i < 0 {
		t.Fatalf("no signature in %q", a)
	}
	return a[i+len("Signature="):]
}

func TestSignGetObjectExample(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	awsExample.sign(req, awsExample.now())
	if got, want := signature(t, req), "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"; got != want {
		t.Errorf("GET Object signature\n got %s\nwant %s", got, want)
	}
	if !strings.Contains(req.Header.Get("Authorization"), "SignedHeaders=host;range;x-amz-content-sha256;x-amz-date") {
		t.Errorf("signed headers: %s", req.Header.Get("Authorization"))
	}
}

func TestSignListObjectsExample(t *testing.T) {
	req, err := awsExample.request(context.Background(), "", url.Values{"max-keys": {"2"}, "prefix": {"J"}})
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J" {
		t.Errorf("URL %s", req.URL)
	}
	if got, want := signature(t, req), "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"; got != want {
		t.Errorf("GET Bucket signature\n got %s\nwant %s", got, want)
	}
}

// Path style puts the bucket in the path, which is what MinIO and most
// non-AWS stores want; a subresource without a value is sent as "name=".
func TestPathStyleAndSubresources(t *testing.T) {
	c := awsExample
	c.Endpoint, c.PathStyle = "http://minio:9000", true
	req, err := c.request(context.Background(), "anchors/main/0001.json", url.Values{"retention": {""}, "versionId": {"v 1"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != "http://minio:9000/examplebucket/anchors/main/0001.json?retention=&versionId=v%201" {
		t.Errorf("URL %s", got)
	}
	req, _ = c.request(context.Background(), "", url.Values{"versions": {""}, "prefix": {"anchors/main/"}})
	if got := req.URL.String(); got != "http://minio:9000/examplebucket?prefix=anchors%2Fmain%2F&versions=" {
		t.Errorf("URL %s", got)
	}
}

// A listing with versions, a delete marker and a second page, served the way
// S3 serves it.
func TestListVersions(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		pages++
		if r.URL.Query().Get("key-marker") == "" {
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<ListVersionsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <IsTruncated>true</IsTruncated><NextKeyMarker>a/2.json</NextKeyMarker><NextVersionIdMarker>v2</NextVersionIdMarker>
  <Version><Key>a/1.json</Key><VersionId>v1</VersionId><IsLatest>false</IsLatest><LastModified>2026-10-16T10:00:00.000Z</LastModified><Size>300</Size></Version>
  <DeleteMarker><Key>a/1.json</Key><VersionId>dm</VersionId><IsLatest>true</IsLatest><LastModified>2026-10-16T11:00:00.000Z</LastModified></DeleteMarker>
</ListVersionsResult>`))
			return
		}
		if r.URL.Query().Get("version-id-marker") != "v2" {
			t.Errorf("second page asked with %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`<ListVersionsResult><IsTruncated>false</IsTruncated>
  <Version><Key>a/2.json</Key><VersionId>v2</VersionId><IsLatest>true</IsLatest><LastModified>2026-10-16T12:00:00Z</LastModified><Size>310</Size></Version>
</ListVersionsResult>`))
	}))
	defer srv.Close()
	c := Config{Endpoint: srv.URL, Region: "us-east-1", Bucket: "b", AccessKey: "k", SecretKey: "s", PathStyle: true}
	vs, err := c.ListVersions(context.Background(), "a/")
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 || len(vs) != 3 {
		t.Fatalf("pages %d, versions %+v", pages, vs)
	}
	var markers int
	for _, v := range vs {
		if v.DeleteMarker {
			markers++
			if v.Key != "a/1.json" || !v.IsLatest {
				t.Errorf("marker %+v", v)
			}
		}
	}
	if markers != 1 {
		t.Errorf("%d delete markers", markers)
	}
}

// A refusal comes back as the store's own code and message, and an object
// without Object Lock has no retention rather than an error.
func TestErrorsAndRetention(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/denied.json"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code><Message>Access Denied.</Message></Error>`))
		case strings.HasSuffix(r.URL.Path, "/open.json"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<Error><Code>NoSuchObjectLockConfiguration</Code><Message>none</Message></Error>`))
		default:
			_, _ = w.Write([]byte(`<Retention><Mode>COMPLIANCE</Mode><RetainUntilDate>2027-10-16T00:00:00Z</RetainUntilDate></Retention>`))
		}
	}))
	defer srv.Close()
	c := Config{Endpoint: srv.URL, Region: "us-east-1", Bucket: "b", AccessKey: "k", SecretKey: "s", PathStyle: true}
	ctx := context.Background()
	if _, err := c.GetVersion(ctx, "denied.json", ""); err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Errorf("refusal: %v", err)
	}
	if mode, _, err := c.Retention(ctx, "open.json", "v"); err != nil || mode != "" {
		t.Errorf("no lock: %q %v", mode, err)
	}
	mode, until, err := c.Retention(ctx, "locked.json", "v")
	if err != nil || mode != "COMPLIANCE" || until.Year() != 2027 {
		t.Errorf("locked: %q %v %v", mode, until, err)
	}
}
