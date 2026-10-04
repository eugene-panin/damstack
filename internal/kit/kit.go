// Package kit packs a project and its vault password into one file, encrypted
// with age under a passphrase, to bring the project back on another machine.
package kit

import (
	"archive/tar"
	"compress/gzip"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/eugene-panin/damstack/internal/project"
)

const (
	projectPrefix = "project/"
	passwordName  = "vault-pass"
)

// Skip is whether a path of the project stays out of a kit: the work
// directory holds caches, plans and logs damstack makes again, and git keeps
// its own history.
func Skip(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, dir := range []string{project.WorkDir, ".git"} {
		if rel == dir || strings.HasPrefix(rel, dir+"/") {
			return true
		}
	}
	return false
}

// Passphrase is a new random passphrase of 100 bits, in five groups of four
// characters that are easy to write down and type.
func Passphrase() string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	b := make([]byte, 20)
	rand.Read(b)
	var out strings.Builder
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			out.WriteByte('-')
		}
		out.WriteByte(alphabet[int(c)%len(alphabet)])
	}
	return out.String()
}

// Pack writes the files of the project in dir, and its vault password, to w,
// encrypted under passphrase.
func Pack(w io.Writer, dir, password, passphrase string) error {
	r, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return err
	}
	enc, err := age.Encrypt(w, r)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(enc)
	tw := tar.NewWriter(gz)
	now := time.Now()
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		if Skip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = projectPrefix + filepath.ToSlash(rel)
		hdr.Uname, hdr.Gname, hdr.Uid, hdr.Gid = "", "", 0, 0
		if d.IsDir() {
			hdr.Name += "/"
			return tw.WriteHeader(hdr)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return err
	}
	pass := []byte(password + "\n")
	if err := tw.WriteHeader(&tar.Header{Name: passwordName, Mode: 0o600, Size: int64(len(pass)), ModTime: now, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := tw.Write(pass); err != nil {
		return err
	}
	return errors.Join(tw.Close(), gz.Close(), enc.Close())
}

// ErrPassphrase is a kit that the passphrase given does not open.
var ErrPassphrase = errors.New("the passphrase does not open this kit")

// Unpack decrypts a kit from r into dest, which must not exist, and returns
// the vault password it holds.
func Unpack(r io.Reader, passphrase, dest string) (string, error) {
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return "", err
	}
	dec, err := age.Decrypt(r, id)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return "", ErrPassphrase
		}
		return "", fmt.Errorf("not a kit of damstack: %w", err)
	}
	gz, err := gzip.NewReader(dec)
	if err != nil {
		return "", fmt.Errorf("not a kit of damstack: %w", err)
	}
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("%s is there already; damstack does not write over it", dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dest), "."+filepath.Base(dest)+"-")
	if err != nil {
		return "", err
	}
	password, err := extract(tar.NewReader(gz), tmp)
	if err == nil {
		err = os.Chmod(tmp, 0o755)
	}
	if err == nil {
		err = os.Rename(tmp, dest)
	}
	if err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	return password, nil
}

func extract(tr *tar.Reader, dest string) (string, error) {
	password := ""
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("the kit is damaged: %w", err)
		}
		if hdr.Name == passwordName {
			data, err := io.ReadAll(io.LimitReader(tr, 4096))
			if err != nil {
				return "", err
			}
			password = strings.TrimSpace(string(data))
			continue
		}
		rel, ok := strings.CutPrefix(hdr.Name, projectPrefix)
		rel = strings.TrimSuffix(rel, "/")
		if !ok || rel == "" || !filepath.IsLocal(rel) || path.Clean(rel) != rel {
			return "", fmt.Errorf("the kit holds %q, outside a project", hdr.Name)
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		mode := fs.FileMode(hdr.Mode).Perm()
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, mode|0o700); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", err
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return "", err
			}
			_, err = io.Copy(f, tr)
			if err := errors.Join(err, f.Close()); err != nil {
				return "", err
			}
			os.Chtimes(target, hdr.ModTime, hdr.ModTime)
		default:
			return "", fmt.Errorf("the kit holds %q, not a file or a directory", hdr.Name)
		}
	}
	if password == "" {
		return "", errors.New("the kit holds no vault password")
	}
	return password, nil
}

// Changed is the newest time a file the kit of a project must follow was
// changed: the settings, the secrets, the state and the certificate.
func Changed(dir string) time.Time {
	var newest time.Time
	consider := func(p string) {
		if info, err := os.Stat(p); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	for _, name := range []string{project.ConfigFile, project.VaultFile, "ca.pem", project.MetaFile} {
		consider(filepath.Join(dir, name))
	}
	states, _ := filepath.Glob(filepath.Join(dir, "state", "*.tfstate"))
	for _, p := range states {
		consider(p)
	}
	return newest
}
