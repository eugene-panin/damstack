package main

import (
	"context"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/secret"
)

// trustFile records the CA damstack made this Mac trust for a project, so
// that it can take it back, or replace it after the CA changes.
const trustFile = ".damstack/trust.json"

type trusted struct {
	Cert string `json:"cert"`
}

func trustCommand(s *streams) *cobra.Command {
	var remove bool
	cmd := &cobra.Command{
		Use:   "trust [project]",
		Short: "Make this Mac trust the CA of a project, so that its servers open in the browser on their own addresses",
		Long: "Adds the CA of the project to the login keychain, trusted for TLS only. The CA signs for the names\n" +
			"of the agents on the private network only, so trusting it lets nobody impersonate any other site.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := pickProject(s, firstArg(args))
			if err != nil {
				return err
			}
			m, _, err := projectStack(cmd.Context(), p, "")
			if err != nil {
				return err
			}
			if remove {
				if err := untrust(cmd.Context(), p); err != nil {
					return err
				}
				fmt.Fprintf(s.out, "This Mac no longer trusts the CA of %s.\n", p.Meta.Name)
				return nil
			}
			return trust(cmd.Context(), s, p, m)
		},
	}
	cmd.Flags().BoolVar(&remove, "remove", false, "take the trust back")
	return cmd
}

// caPath is the certificate of the CA a stack generates for a project, empty
// when it generates none.
func caPath(p *project.Project, m *manifest.Manifest) string {
	for _, sec := range m.Secrets {
		if sec.Generate == "ca" && sec.Cert != "" {
			return filepath.Join(p.Dir, sec.Cert)
		}
	}
	return ""
}

func caSecret(m *manifest.Manifest) string {
	for _, sec := range m.Secrets {
		if sec.Generate == "ca" && sec.Cert != "" {
			return sec.Name
		}
	}
	return ""
}

func readCert(path string) (*x509.Certificate, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, nil, fmt.Errorf("%s holds no certificate", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	return cert, data, err
}

func trust(ctx context.Context, s *streams, p *project.Project, m *manifest.Manifest) error {
	path := caPath(p, m)
	if path == "" {
		return fmt.Errorf("the stack %s makes no CA for %s", m.Name, p.Meta.Name)
	}
	cert, data, err := readCert(path)
	if err != nil {
		return err
	}
	if !secret.Constrained(cert) {
		return fmt.Errorf("the CA of %s may sign for any name: whoever holds its key could then impersonate any site to this Mac. "+
			"Make a CA that signs for the private network only, and trust that one: "+
			"damstack secret rotate %s -p %s, then damstack deploy %s", p.Meta.Name, caSecret(m), p.Meta.Name, p.Meta.Name)
	}
	if err := untrust(ctx, p); err != nil {
		return err
	}
	keychain, err := loginKeychain()
	if err != nil {
		return err
	}
	fmt.Fprintln(s.out, "macOS asks for your password, or Touch ID, to change what this Mac trusts.")
	out, err := exec.CommandContext(ctx, "security", "add-trusted-cert", "-r", "trustRoot", "-p", "ssl", "-k", keychain, path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("security add-trusted-cert: %s", strings.TrimSpace(string(out)))
	}
	rec, err := json.Marshal(trusted{Cert: string(data)})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(p.Dir, trustFile), append(rec, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "This Mac trusts the CA of %s, for the servers of its private network only.\n", p.Meta.Name)
	return nil
}

// untrust takes back the CA damstack made this Mac trust for p, if any.
func untrust(ctx context.Context, p *project.Project) error {
	data, err := os.ReadFile(filepath.Join(p.Dir, trustFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var t trusted
	if err := json.Unmarshal(data, &t); err != nil {
		return err
	}
	block, _ := pem.Decode([]byte(t.Cert))
	if block != nil {
		tmp, err := os.CreateTemp("", "damstack-ca-*.pem")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		tmp.WriteString(t.Cert)
		tmp.Close()
		exec.CommandContext(ctx, "security", "remove-trusted-cert", tmp.Name()).Run()
		sum := sha1.Sum(block.Bytes)
		if keychain, err := loginKeychain(); err == nil {
			exec.CommandContext(ctx, "security", "delete-certificate", "-Z", strings.ToUpper(hex.EncodeToString(sum[:])), keychain).Run()
		}
	}
	return os.Remove(filepath.Join(p.Dir, trustFile))
}

func loginKeychain() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Keychains", "login.keychain-db"), nil
}

// trustNotice says what to do about the trust of the CA of p after a deploy:
// replace it when the CA changed, or offer it once.
func trustNotice(ctx context.Context, s *streams, p *project.Project, m *manifest.Manifest) {
	path := caPath(p, m)
	if path == "" {
		return
	}
	_, current, err := readCert(path)
	if err != nil {
		return
	}
	data, err := os.ReadFile(filepath.Join(p.Dir, trustFile))
	if err == nil {
		var t trusted
		if json.Unmarshal(data, &t) == nil && t.Cert != string(current) {
			fmt.Fprintf(s.out, "\nThe CA of %s changed, and this Mac trusts the old one.\n", p.Meta.Name)
			ok, err := s.confirm("Trust the new one instead?", true)
			if err == nil && ok {
				if err := trust(ctx, s, p, m); err != nil {
					fmt.Fprintf(s.err, "damstack: %v\n", err)
				}
				return
			}
			fmt.Fprintf(s.out, "damstack trust %s does it later.\n", p.Meta.Name)
		}
		return
	}
	offered := filepath.Join(p.Dir, ".damstack", "trust-offered")
	if _, err := os.Stat(offered); err == nil {
		return
	}
	os.WriteFile(offered, nil, 0o644)
	fmt.Fprintf(s.out, "\nThe servers of %s also open in the browser on their own addresses, without the admin pages,\n"+
		"once this Mac trusts the CA of the project: damstack trust %s\n", p.Meta.Name, p.Meta.Name)
}
