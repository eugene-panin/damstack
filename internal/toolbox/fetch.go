package toolbox

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// URL is where the archive of a release of the toolbox for a kind of Mac is.
func URL(version, arch string) string {
	return fmt.Sprintf("https://github.com/eugene-panin/damstack-toolbox/releases/download/v%s/damstack-toolbox-%s-darwin-%s.tar.gz",
		version, version, arch)
}

// Dir is where a release of the toolbox is unpacked under cache.
func Dir(cache, version string) string { return filepath.Join(cache, "toolbox", version) }

// Have is whether a release of the toolbox is unpacked under cache.
func Have(cache, version string) bool {
	_, err := os.Stat(filepath.Join(Dir(cache, version), "VERSION"))
	return err == nil
}

// Fetch unpacks a release of the toolbox under cache, unless it is there,
// checked against the SHA-256 of its archive for this Mac, and returns its
// directory.
func Fetch(ctx context.Context, cache, version string, sums map[string]string) (string, error) {
	dir := Dir(cache, version)
	if Have(cache, version) {
		return dir, nil
	}
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("the toolbox is built for macOS only, and this is %s", runtime.GOOS)
	}
	want, ok := sums[runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("the toolbox has no build for %s", runtime.GOARCH)
	}
	url := URL(version, runtime.GOARCH)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	archive, err := os.CreateTemp(filepath.Dir(dir), ".download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	if err := download(ctx, url, archive, want); err != nil {
		return "", err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".unpack-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := unpack(archive, tmp); err != nil {
		return "", fmt.Errorf("unpack %s: %w", url, err)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		if Have(cache, version) {
			return dir, nil
		}
		return "", err
	}
	return dir, nil
}

func download(ctx context.Context, url string, w io.Writer, want string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch the toolbox: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch the toolbox: %s from %s", resp.Status, url)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, 512<<20)); err != nil {
		return fmt.Errorf("fetch the toolbox: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("the toolbox from %s does not match its SHA-256: got %s, want %s", url, got, want)
	}
	return nil
}

// unpack writes a gzipped tar into dest: directories, files and symbolic
// links that stay inside it.
func unpack(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if name == "." {
			continue
		}
		if !filepath.IsLocal(name) {
			return fmt.Errorf("%q is outside the toolbox", hdr.Name)
		}
		target := filepath.Join(dest, name)
		mode := fs.FileMode(hdr.Mode).Perm()
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, mode|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if err := errors.Join(err, f.Close()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			link := filepath.Join(filepath.Dir(name), hdr.Linkname)
			if filepath.IsAbs(hdr.Linkname) || !filepath.IsLocal(link) {
				return fmt.Errorf("%q links outside the toolbox, to %q", hdr.Name, hdr.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%q is not a file, a directory or a link", hdr.Name)
		}
	}
}
