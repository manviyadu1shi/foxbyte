// SPDX-License-Identifier: AGPL-3.0-or-later

// Package s3read reads an S3-compatible bucket: every version of every object
// under a prefix, one version's bytes, and one version's Object Lock
// retention. Nothing else, and never a write.
//
// It exists for fox-verify, which an auditor runs on their own machine with
// read-only keys, against the bucket anchors are locked in. It is written on
// the standard library rather than an SDK so that the verifier stays one small
// binary with nothing to trust but itself: requests are signed with AWS
// Signature Version 4, which every S3-compatible store accepts.
package s3read

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Config says how to reach a bucket.
type Config struct {
	Endpoint  string // https://s3.eu-west-1.amazonaws.com, or a MinIO URL
	Region    string // us-east-1 for most non-AWS stores
	Bucket    string
	AccessKey string
	SecretKey string
	PathStyle bool // bucket in the path, not the host name (MinIO and most non-AWS stores)
	// HTTP is the client to use; nil means one with a 60-second timeout.
	HTTP *http.Client
	// now is the clock, for tests.
	now func() time.Time
}

// Version is one version of an object, or a delete marker.
type Version struct {
	Key          string
	VersionID    string
	IsLatest     bool
	LastModified time.Time
	Size         int64
	DeleteMarker bool
}

// emptyHash is the SHA-256 of an empty body, which is every request here.
const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// ListVersions returns every version and delete marker whose key begins with
// prefix, following the listing's pages.
func (c Config) ListVersions(ctx context.Context, prefix string) ([]Version, error) {
	var out []Version
	keyMarker, versionMarker := "", ""
	for page := 0; page < 10000; page++ {
		q := url.Values{"versions": {""}, "prefix": {prefix}}
		if keyMarker != "" {
			q.Set("key-marker", keyMarker)
			q.Set("version-id-marker", versionMarker)
		}
		body, err := c.get(ctx, "", q)
		if err != nil {
			return nil, err
		}
		var r listVersionsResult
		if err := xml.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("reading the version listing: %w", err)
		}
		for _, v := range r.Versions {
			out = append(out, Version{Key: v.Key, VersionID: v.VersionID, IsLatest: v.IsLatest, LastModified: v.LastModified, Size: v.Size})
		}
		for _, m := range r.DeleteMarkers {
			out = append(out, Version{Key: m.Key, VersionID: m.VersionID, IsLatest: m.IsLatest, LastModified: m.LastModified, DeleteMarker: true})
		}
		if !r.IsTruncated {
			return out, nil
		}
		if r.NextKeyMarker == "" {
			return nil, errors.New("the version listing is truncated but names no next page")
		}
		keyMarker, versionMarker = r.NextKeyMarker, r.NextVersionIDMarker
	}
	return nil, errors.New("the version listing did not end")
}

// GetVersion returns the bytes of one version of an object.
func (c Config) GetVersion(ctx context.Context, key, versionID string) ([]byte, error) {
	q := url.Values{}
	if versionID != "" {
		q.Set("versionId", versionID)
	}
	return c.get(ctx, key, q)
}

// Retention is one version's Object Lock retention: its mode (COMPLIANCE or
// GOVERNANCE, "" when it has none) and the time it ends.
func (c Config) Retention(ctx context.Context, key, versionID string) (mode string, until time.Time, err error) {
	q := url.Values{"retention": {""}}
	if versionID != "" {
		q.Set("versionId", versionID)
	}
	body, err := c.get(ctx, key, q)
	if err != nil {
		var e *Error
		if errors.As(err, &e) && (e.Code == "NoSuchObjectLockConfiguration" || e.Code == "ObjectLockConfigurationNotFoundError") {
			return "", time.Time{}, nil
		}
		return "", time.Time{}, err
	}
	var r struct {
		Mode            string    `xml:"Mode"`
		RetainUntilDate time.Time `xml:"RetainUntilDate"`
	}
	if err := xml.Unmarshal(body, &r); err != nil {
		return "", time.Time{}, fmt.Errorf("reading the retention of %s: %w", key, err)
	}
	return strings.ToUpper(r.Mode), r.RetainUntilDate, nil
}

// Error is what the store answered when it refused.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("%s: %s (HTTP %d)", e.Code, e.Message, e.Status)
}

type listVersionsResult struct {
	IsTruncated         bool   `xml:"IsTruncated"`
	NextKeyMarker       string `xml:"NextKeyMarker"`
	NextVersionIDMarker string `xml:"NextVersionIdMarker"`
	Versions            []struct {
		Key          string    `xml:"Key"`
		VersionID    string    `xml:"VersionId"`
		IsLatest     bool      `xml:"IsLatest"`
		LastModified time.Time `xml:"LastModified"`
		Size         int64     `xml:"Size"`
	} `xml:"Version"`
	DeleteMarkers []struct {
		Key          string    `xml:"Key"`
		VersionID    string    `xml:"VersionId"`
		IsLatest     bool      `xml:"IsLatest"`
		LastModified time.Time `xml:"LastModified"`
	} `xml:"DeleteMarker"`
}

// maxBody bounds what one answer may be: an anchor is a few hundred bytes and
// a listing page at most a thousand entries.
const maxBody = 16 << 20

// get sends a signed GET for key ("" is the bucket itself) with query q.
func (c Config) get(ctx context.Context, key string, q url.Values) ([]byte, error) {
	req, err := c.request(ctx, key, q)
	if err != nil {
		return nil, err
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		e := &Error{Status: resp.StatusCode}
		var x struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		}
		if xml.Unmarshal(body, &x) == nil {
			e.Code, e.Message = x.Code, x.Message
		}
		return nil, e
	}
	return body, nil
}

// request builds and signs the GET.
func (c Config) request(ctx context.Context, key string, q url.Values) (*http.Request, error) {
	ep, err := url.Parse(strings.TrimRight(c.Endpoint, "/"))
	if err != nil || ep.Host == "" || (ep.Scheme != "https" && ep.Scheme != "http") {
		return nil, fmt.Errorf("the endpoint must be an http(s) URL, not %q", c.Endpoint)
	}
	host := ep.Host
	rawPath := "/"
	if c.PathStyle {
		rawPath = "/" + c.Bucket + "/"
	} else {
		host = c.Bucket + "." + ep.Host
	}
	if key != "" {
		rawPath += key
	} else if c.PathStyle {
		rawPath = "/" + c.Bucket
	}
	u := &url.URL{Scheme: ep.Scheme, Host: host, Path: rawPath, RawPath: uriEncode(rawPath, false), RawQuery: canonicalQuery(q)}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	c.sign(req, now().UTC())
	return req, nil
}

// sign adds AWS Signature Version 4 headers to req: host, every x-amz-*
// header and any other header already set are signed.
func (c Config) sign(req *http.Request, t time.Time) {
	amzDate := t.Format("20060102T150405Z")
	day := t.Format("20060102")
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", emptyHash)

	names := []string{"host"}
	values := map[string]string{"host": req.URL.Host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		names = append(names, lk)
		values[lk] = strings.Join(strings.Fields(strings.Join(v, ",")), " ")
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		canonHeaders.WriteString(n + ":" + values[n] + "\n")
	}
	signed := strings.Join(names, ";")

	canonical := strings.Join([]string{
		req.Method,
		uriEncode(req.URL.Path, false),
		req.URL.RawQuery,
		canonHeaders.String(),
		signed,
		emptyHash,
	}, "\n")
	scope := day + "/" + c.Region + "/s3/aws4_request"
	h := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(h[:])

	k := hmacSHA256([]byte("AWS4"+c.SecretKey), day)
	k = hmacSHA256(k, c.Region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.AccessKey, scope, signed, sig))
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// canonicalQuery is the query string as Signature Version 4 wants it: sorted
// by name, names and values encoded, a parameter without a value as "name=".
// It is also what is sent, so the two cannot differ.
func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

// uriEncode percent-encodes everything but the unreserved characters, as
// Signature Version 4 specifies; '/' too unless it separates a path.
func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9',
			ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteByte(ch)
		case ch == '/' && !encodeSlash:
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}
