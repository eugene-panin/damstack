// Package vault reads and writes Ansible Vault files, format 1.1 with AES256,
// so that damstack keeps the secrets of a project in the file Ansible reads.
package vault

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const header = "$ANSIBLE_VAULT;1.1;AES256"

var ErrWrongPassword = errors.New("the vault password does not open this file")

// Encrypt returns plaintext as an Ansible Vault file.
func Encrypt(plaintext []byte, password string) ([]byte, error) {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	cipherKey, hmacKey, iv, err := keys(password, salt)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cipherKey)
	if err != nil {
		return nil, err
	}
	padded := pad(plaintext)
	ciphertext := make([]byte, len(padded))
	cipher.NewCTR(block, iv).XORKeyStream(ciphertext, padded)
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write(ciphertext)

	body := hex.EncodeToString([]byte(hex.EncodeToString(salt) + "\n" + hex.EncodeToString(mac.Sum(nil)) + "\n" + hex.EncodeToString(ciphertext)))
	var out bytes.Buffer
	out.WriteString(header + "\n")
	for len(body) > 80 {
		out.WriteString(body[:80] + "\n")
		body = body[80:]
	}
	out.WriteString(body + "\n")
	return out.Bytes(), nil
}

// Decrypt opens an Ansible Vault file.
func Decrypt(file []byte, password string) ([]byte, error) {
	lines := strings.Split(strings.TrimSpace(string(file)), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "$ANSIBLE_VAULT;1.1;AES256") {
		return nil, errors.New("not an Ansible Vault 1.1 AES256 file")
	}
	outer, err := hex.DecodeString(strings.Join(lines[1:], ""))
	if err != nil {
		return nil, fmt.Errorf("vault body: %w", err)
	}
	parts := strings.Split(string(outer), "\n")
	if len(parts) != 3 {
		return nil, errors.New("vault body: want salt, hmac and ciphertext")
	}
	var fields [3][]byte
	for i, p := range parts {
		if fields[i], err = hex.DecodeString(p); err != nil {
			return nil, fmt.Errorf("vault body: %w", err)
		}
	}
	salt, sum, ciphertext := fields[0], fields[1], fields[2]
	cipherKey, hmacKey, iv, err := keys(password, salt)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write(ciphertext)
	if !hmac.Equal(mac.Sum(nil), sum) {
		return nil, ErrWrongPassword
	}
	block, err := aes.NewCipher(cipherKey)
	if err != nil {
		return nil, err
	}
	padded := make([]byte, len(ciphertext))
	cipher.NewCTR(block, iv).XORKeyStream(padded, ciphertext)
	return unpad(padded)
}

// keys derives the AES key, the HMAC key and the counter of a vault the way
// Ansible does: 80 bytes of PBKDF2-SHA256, 10000 rounds.
func keys(password string, salt []byte) (cipherKey, hmacKey, iv []byte, err error) {
	derived, err := pbkdf2.Key(sha256.New, password, salt, 10000, 80)
	if err != nil {
		return nil, nil, nil, err
	}
	return derived[:32], derived[32:64], derived[64:80], nil
}

func pad(b []byte) []byte {
	n := aes.BlockSize - len(b)%aes.BlockSize
	return append(bytes.Clone(b), bytes.Repeat([]byte{byte(n)}, n)...)
}

func unpad(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("vault: empty plaintext")
	}
	n := int(b[len(b)-1])
	if n == 0 || n > aes.BlockSize || n > len(b) || !bytes.Equal(b[len(b)-n:], bytes.Repeat([]byte{byte(n)}, n)) {
		return nil, errors.New("vault: bad padding")
	}
	return b[:len(b)-n], nil
}
