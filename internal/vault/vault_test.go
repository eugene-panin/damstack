package vault

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestDecryptsWhatAnsibleVaultWrote(t *testing.T) {
	file, err := os.ReadFile("testdata/ansible.yml")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/ansible.plain.yml")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decrypt(file, "fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if _, err := Decrypt(file, "another-password"); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("a wrong password gave %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, plain := range []string{"", "x", "exactly sixteen!", strings.Repeat("secret\n", 100)} {
		file, err := Encrypt([]byte(plain), "pw")
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(file)), "\n")
		if lines[0] != header {
			t.Errorf("header %q", lines[0])
		}
		for _, l := range lines[1:] {
			if len(l) > 80 {
				t.Errorf("a line of %d characters; Ansible writes 80", len(l))
			}
		}
		got, err := Decrypt(file, "pw")
		if err != nil || string(got) != plain {
			t.Errorf("round trip of %d bytes: %q, %v", len(plain), got, err)
		}
	}
}

func TestEveryEncryptionIsSalted(t *testing.T) {
	a, _ := Encrypt([]byte("same"), "pw")
	b, _ := Encrypt([]byte("same"), "pw")
	if bytes.Equal(a, b) {
		t.Error("two encryptions of the same text are equal")
	}
}

func TestTamperedFileIsRefused(t *testing.T) {
	file, _ := Encrypt([]byte("secret"), "pw")
	lines := strings.Split(string(file), "\n")
	last := []byte(lines[len(lines)-2])
	last[len(last)-1] ^= 1
	lines[len(lines)-2] = string(last)
	if _, err := Decrypt([]byte(strings.Join(lines, "\n")), "pw"); err == nil {
		t.Error("a changed ciphertext decrypted")
	}
}
