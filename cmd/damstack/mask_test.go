package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestALogKeepsNoSecret(t *testing.T) {
	var out bytes.Buffer
	m := newMasker(&out, map[string]any{
		"token":  "3f9a1c0e7b2d",
		"short":  "tok",
		"keys":   []any{"unseal-key-one"},
		"ca_key": "-----BEGIN KEY-----\nMIIEsecretbody\n-----END KEY-----",
		"nested": map[string]any{"pw": "hunter2hunter2"},
	})
	for _, chunk := range []string{"token=3f9a", "1c0e7b2d ok\n", "key unseal-key-one\nbody MIIEsecretbody\n", "pw hunter2hunter2 tok"} {
		m.Write([]byte(chunk))
	}
	m.Flush()
	got := out.String()
	for _, secret := range []string{"3f9a1c0e7b2d", "unseal-key-one", "MIIEsecretbody", "hunter2hunter2"} {
		if strings.Contains(got, secret) {
			t.Errorf("%q is in the log:\n%s", secret, got)
		}
	}
	if !strings.Contains(got, "token="+masked+" ok") || !strings.Contains(got, " tok") {
		t.Errorf("the rest of the log must stay:\n%s", got)
	}
}
