package secret

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"regexp"
	"testing"
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
