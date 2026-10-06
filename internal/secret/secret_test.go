package secret

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net"
	"regexp"
	"testing"
	"time"
)

func TestGenerators(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		n     int
		check func(string) bool
	}{
		{"base64", 0, func(s string) bool { b, err := base64.StdEncoding.DecodeString(s); return err == nil && len(b) == 32 }},
		{"hex", 16, func(s string) bool { b, err := hex.DecodeString(s); return err == nil && len(b) == 16 }},
		{"password", 0, func(s string) bool {
			b, err := base64.RawURLEncoding.DecodeString(s)
			return err == nil && len(b) == 32
		}},
		{"uuid", 0, regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString},
	} {
		a, err := Generate(tc.kind, tc.n, "")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := Generate(tc.kind, tc.n, "")
		if !tc.check(a.Value) || a.Value == b.Value {
			t.Errorf("%s: %q then %q", tc.kind, a.Value, b.Value)
		}
	}
	if _, err := Generate("random", 0, ""); err == nil {
		t.Error("an unknown generator was accepted")
	}
}

func TestCA(t *testing.T) {
	g, err := Generate("ca", 0, "demo")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(g.Cert)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	keyBlock, _ := pem.Decode([]byte(g.Value))
	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !cert.IsCA || cert.Subject.CommonName != "demo CA" || !key.(*ecdsa.PrivateKey).PublicKey.Equal(cert.PublicKey) {
		t.Errorf("ca %v, key does not match: %v", cert.Subject, !key.(*ecdsa.PrivateKey).PublicKey.Equal(cert.PublicKey))
	}
	if err := cert.CheckSignatureFrom(cert); err != nil {
		t.Error(err)
	}
}

func TestCASignsForTheAgentsOnly(t *testing.T) {
	g, err := Generate("ca", 0, "demo")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(g.Cert)
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !Constrained(caCert) || len(caCert.DNSNames) != 0 {
		t.Fatalf("constraints %v %v, names %v", caCert.PermittedDNSDomains, caCert.PermittedIPRanges, caCert.DNSNames)
	}
	keyBlock, _ := pem.Decode([]byte(g.Value))
	caKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	sign := func(dns []string, ips []net.IP) error {
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "leaf"},
			NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), DNSNames: dns, IPAddresses: ips,
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &leafKey.PublicKey, caKey)
		if err != nil {
			return err
		}
		leaf, _ := x509.ParseCertificate(der)
		_, err = leaf.Verify(x509.VerifyOptions{Roots: roots})
		return err
	}
	if err := sign([]string{"server.dc1.consul", "vault.service.consul", "server.global.nomad", "localhost"},
		[]net.IP{net.ParseIP("10.77.0.1"), net.ParseIP("127.0.0.1")}); err != nil {
		t.Errorf("the certificate of an agent: %v", err)
	}
	for name, tc := range map[string]struct {
		dns []string
		ips []net.IP
	}{
		"a public name": {dns: []string{"www.google.com"}},
		"a public IP":   {ips: []net.IP{net.ParseIP("142.250.1.1")}},
		"a home router": {ips: []net.IP{net.ParseIP("192.168.0.1")}},
	} {
		if err := sign(tc.dns, tc.ips); err == nil {
			t.Errorf("%s: the CA signed it", name)
		}
	}
}
