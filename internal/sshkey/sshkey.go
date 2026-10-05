// Package sshkey makes the SSH key of a project, kept in its vault, and
// writes it where ssh reads it.
package sshkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

// Comment marks the key of a project in authorized_keys, so that a rotation
// replaces it and leaves every other key alone.
const Comment = "damstack-project-key"

// Key is the SSH key of a project.
type Key struct {
	PrivateKey string    `yaml:"private_key"`
	PublicKey  string    `yaml:"public_key"`
	Created    time.Time `yaml:"created"`
}

// New makes an ed25519 key.
func New(now time.Time) (*Key, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, Comment)
	if err != nil {
		return nil, err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return &Key{
		PrivateKey: string(pem.EncodeToMemory(block)),
		PublicKey:  strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " " + Comment,
		Created:    now.UTC(),
	}, nil
}

// FromSecret reads the key from the value of its secret; nil when there is
// none yet.
func FromSecret(v any) (*Key, error) {
	if v == nil {
		return nil, nil
	}
	data, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	k := &Key{}
	if err := yaml.Unmarshal(data, k); err != nil {
		return nil, fmt.Errorf("the SSH key of the project: %w", err)
	}
	if k.PrivateKey == "" {
		return nil, nil
	}
	return k, nil
}

// Secret is the key as the value of its secret.
func (k *Key) Secret() (map[string]any, error) {
	data, err := yaml.Marshal(k)
	if err != nil {
		return nil, err
	}
	var v map[string]any
	return v, yaml.Unmarshal(data, &v)
}

// Write puts the private key at path, readable by its owner only, and the
// public key next to it, as ssh-keygen would.
func (k *Key) Write(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(k.PrivateKey), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	return os.WriteFile(path+".pub", []byte(k.PublicKey+"\n"), 0o644)
}
