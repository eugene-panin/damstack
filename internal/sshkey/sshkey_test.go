package sshkey

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestKey(t *testing.T) {
	k, err := New(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(k.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " " + Comment; got != k.PublicKey {
		t.Errorf("the public key %q is not of the private one, %q", k.PublicKey, got)
	}
	v, _ := k.Secret()
	back, err := FromSecret(v)
	if err != nil || back.PrivateKey != k.PrivateKey || !back.Created.Equal(k.Created) {
		t.Fatalf("round trip: %+v, %v", back, err)
	}
	if none, err := FromSecret(nil); none != nil || err != nil {
		t.Errorf("no secret: %+v, %v", none, err)
	}
	path := filepath.Join(t.TempDir(), "ssh", "project-key")
	if err := k.Write(path); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	if out, err := exec.Command("ssh-keygen", "-y", "-P", "", "-f", path).Output(); err != nil || !strings.HasPrefix(k.PublicKey, strings.TrimSpace(string(out))) {
		t.Errorf("ssh-keygen reads %q, %v", out, err)
	}
}
