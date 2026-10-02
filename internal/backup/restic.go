// Package backup pulls the restic backups a server makes to this machine, and
// runs the pulls on a schedule.
package backup

import (
	"bytes"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// ResticVersion is the restic damstack runs on this machine, the one the
// servers run.
const ResticVersion = "0.19.1"

var resticSHA256 = map[string]string{
	"darwin_amd64": "c38d579622cf602f665234c5a8c315030b6cf70656028fe6dc29a786b60e5f35",
	"darwin_arm64": "7be0a144ccc377880f294204aa271d76e4b79554b42a751151d425ce6ebac143",
	"linux_amd64":  "f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c",
	"linux_arm64":  "a5f64aaab53d51e311fa3829124c5b703f2d14cf187d8640b6be3b2b49376465",
}

// Restic is the path of restic under cache, fetched from its release and
// checked against its SHA-256 the first time.
func Restic(ctx context.Context, cache string) (string, error) {
	bin := filepath.Join(cache, "restic", ResticVersion, "restic")
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	target := runtime.GOOS + "_" + runtime.GOARCH
	want, ok := resticSHA256[target]
	if !ok {
		return "", fmt.Errorf("damstack has no restic for %s", target)
	}
	url := fmt.Sprintf("https://github.com/restic/restic/releases/download/v%s/restic_%s_%s.bz2", ResticVersion, ResticVersion, target)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch restic: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch restic: %s from %s", resp.Status, url)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return "", fmt.Errorf("fetch restic: %w", err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != want {
		return "", fmt.Errorf("the restic from %s does not match its SHA-256: got %s", url, got)
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(bin), "restic-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, bzip2.NewReader(bytes.NewReader(data)))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("unpack restic: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	return bin, os.Rename(tmp.Name(), bin)
}
