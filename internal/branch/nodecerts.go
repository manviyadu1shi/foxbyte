// SPDX-License-Identifier: AGPL-3.0-or-later

package branch

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thefoxbyte/foxbyte/internal/brand"
)

// Certificates for replication between machines.
//
// Phase 1 of docs/FOX_HA_Plan.md asks for "TLS on replication traffic". The
// engine already has internal/tlsutil, and it is deliberately not reused here:
// that package says of itself that the point "is not a trusted chain — it is
// encryption on the wire", which is the right call for a browser reaching the
// console over a self-signed certificate it was told to accept once.
//
// It is the wrong call for replication. A standby that encrypts without
// verifying has an encrypted channel to whoever answered, and what it sends
// down that channel is a credential that can copy the database. So this is a
// small certificate authority: one per install, kept on the primary, issuing a
// server certificate that names the address a standby will dial. The standby
// gets the CA certificate when it joins and connects with sslmode=verify-full.
//
// Measured across two machines before any of it was written:
//
//   - verify-full with the CA: IDENTIFY_SYSTEM answers;
//   - sslmode=disable: refused, because the rule is `hostssl` and that matches
//     only TLS connections — TLS is required rather than merely offered;
//   - verify-full without the CA: the client refuses to trust the server;
//   - the superuser, over TLS, from the permitted address: no pg_hba entry.
//
// And a real pg_basebackup over that connection from the other machine, which
// is the operation this exists for.

// nodeCADir holds the install's CA. Beside the licence and the primary
// pointer, in the 0700 state directory, because the key is the one thing here
// that must not leave this machine.
func nodeCADir() string { return brand.StatePath("tls/node") }

func nodeCACertPath() string { return filepath.Join(nodeCADir(), "ca.crt") }
func nodeCAKeyPath() string  { return filepath.Join(nodeCADir(), "ca.key") }

// NodeCACertPEM is the certificate a standby needs in order to verify this
// primary. It is public by nature: it travels in the join token, and it is the
// only half of the CA that ever does.
func NodeCACertPEM() (string, error) {
	b, err := os.ReadFile(nodeCACertPath())
	if err != nil {
		return "", fmt.Errorf("this install has no replication CA yet — run `%s node expose` first: %w", brand.CLI, err)
	}
	return string(b), nil
}

// ensureNodeCA makes the install's CA if it has none. Ten years, because
// rotating it means re-joining every standby and that is a decision for an
// operator rather than a surprise on a Tuesday.
func ensureNodeCA() error {
	if _, err := os.Stat(nodeCACertPath()); err == nil {
		if _, err := os.Stat(nodeCAKeyPath()); err == nil {
			return nil
		}
	}
	if err := os.MkdirAll(nodeCADir(), 0o700); err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating the CA key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   brand.Product + " replication CA",
			Organization: []string{brand.Product},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("creating the CA certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := writeNodePEM(nodeCACertPath(), "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	return writeNodePEM(nodeCAKeyPath(), "EC PRIVATE KEY", keyDER, 0o600)
}

// serverCertNames is the certificate's identity: what a standby may dial and
// still have verify-full succeed.
//
// Pure, and tested, because getting this wrong fails in the least helpful way
// available — the standby connects, the TLS handshake completes, and the
// certificate is rejected for naming somebody else. An address that parses as
// an IP goes in IPAddresses; anything else is a hostname. localhost and the
// loopback address are always included, so turning this on does not break the
// engine's own local connections.
func serverCertNames(addr string) (dns []string, ips []net.IP) {
	dns = []string{"localhost"}
	ips = []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	if a := strings.TrimSpace(addr); a != "" {
		if ip := net.ParseIP(a); ip != nil {
			ips = append(ips, ip)
		} else {
			dns = append(dns, a)
		}
	}
	return dns, ips
}

// issueServerCert writes the primary's certificate and key into a directory,
// signed by the install's CA and naming addr.
//
// The files go into main's data directory, which the container already
// bind-mounts — so turning TLS on needs no new mount and therefore no restart
// of its own: ssl, ssl_cert_file and ssl_key_file are all sighup settings
// (checked against a live server). Postgres refuses to start with a key group-
// or world-readable, so the key is 0600 and owned by the postgres uid.
func issueServerCert(dir, addr string) (certPath, keyPath string, err error) {
	if err := ensureNodeCA(); err != nil {
		return "", "", err
	}
	caCertPEM, err := os.ReadFile(nodeCACertPath())
	if err != nil {
		return "", "", err
	}
	caKeyPEM, err := os.ReadFile(nodeCAKeyPath())
	if err != nil {
		return "", "", err
	}
	caCert, caKey, err := parseCA(caCertPEM, caKeyPEM)
	if err != nil {
		return "", "", err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	dns, ips := serverCertNames(addr)
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   addr,
			Organization: []string{brand.Product},
		},
		NotBefore: time.Now().Add(-time.Hour),
		// 825 days: the longest a server certificate is widely accepted for,
		// and short enough that an install which outlives it is told rather
		// than trusted for a decade.
		NotAfter:              time.Now().AddDate(0, 0, 825),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dns,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return "", "", fmt.Errorf("signing the server certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	certPath = filepath.Join(dir, nodeServerCertName)
	keyPath = filepath.Join(dir, nodeServerKeyName)
	if err := writeNodePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return "", "", err
	}
	if err := writeNodePEM(keyPath, "EC PRIVATE KEY", keyDER, 0o600); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

// The names the files take inside main's data directory. Prefixed, so they are
// obviously ours beside Postgres's own files and cannot collide with the
// server.crt a future default might look for.
const (
	nodeServerCertName = "fox-replication.crt"
	nodeServerKeyName  = "fox-replication.key"
)

func parseCA(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cb, _ := pem.Decode(certPEM)
	if cb == nil {
		return nil, nil, fmt.Errorf("the CA certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing the CA certificate: %w", err)
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, nil, fmt.Errorf("the CA key is not PEM")
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing the CA key: %w", err)
	}
	return cert, key, nil
}

func writeNodePEM(path, blockType string, der []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Written beside and renamed over, so nothing ever reads half a key.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pem-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := pem.Encode(tmp, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
