// Package secret generates the secrets a stack asks damstack for.
package secret

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

const defaultBytes = 32

// Generated is a new secret; Cert is the certificate of a ca, whose key is
// the Value.
type Generated struct {
	Value string
	Cert  []byte
}

// Generate makes a secret of a kind of manifest.Generators; n is the number
// of random bytes, 32 when 0, and name names the certificate of a ca.
func Generate(kind string, n int, name string) (Generated, error) {
	if n == 0 {
		n = defaultBytes
	}
	switch kind {
	case "base64":
		return Generated{Value: base64.StdEncoding.EncodeToString(random(n))}, nil
	case "hex":
		return Generated{Value: hex.EncodeToString(random(n))}, nil
	case "password":
		return Generated{Value: base64.RawURLEncoding.EncodeToString(random(n))}, nil
	case "uuid":
		b := random(16)
		b[6] = b[6]&0x0f | 0x40
		b[8] = b[8]&0x3f | 0x80
		return Generated{Value: fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])}, nil
	case "ca":
		return ca(name + " CA")
	}
	return Generated{}, fmt.Errorf("%q is not a generator", kind)
}

func random(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func ca(commonName string) (Generated, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Generated{}, err
	}
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return Generated{}, err
	}
	subjectKeyID := sha1.Sum(public)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return Generated{}, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		SubjectKeyId:          subjectKeyID[:],
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return Generated{}, err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return Generated{}, err
	}
	return Generated{
		Value: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})),
		Cert:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}, nil
}
