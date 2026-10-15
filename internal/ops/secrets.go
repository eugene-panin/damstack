package ops

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/eugene-panin/damstack/internal/atomicfile"
	"github.com/eugene-panin/damstack/internal/checks"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/secret"
)

// SecretSource is where a secret of vault.yml comes from: the platform's
// stack or an app's, and its spec there; Spec is zero for one a step of the
// stack keeps, such as Vault's root token.
type SecretSource struct {
	Spec manifest.Secret
	// App is the app it is of, empty for the platform.
	App string
}

// AskedSecrets are the secrets a person gives a project: those its platform and
// its apps ask for.
func AskedSecrets(p *project.Project) []SecretSource {
	var out []SecretSource
	if m, _ := CachedStack(p); m != nil {
		for _, sec := range m.Secrets {
			if sec.Ask != "" {
				out = append(out, SecretSource{sec, ""})
			}
		}
	}
	for _, ref := range p.Meta.Apps {
		if am, _ := CachedRef(ref); am != nil {
			for _, sec := range am.Secrets {
				if sec.Ask != "" {
					out = append(out, SecretSource{sec, ref.Name})
				}
			}
		}
	}
	return out
}

// MakeAnew puts a new value of a generated secret in secrets; put writes its
// certificate, when it has one, in the project, the old one next to it, once
// the secret is kept.
func MakeAnew(p *project.Project, spec manifest.Secret, secrets map[string]any) (put func() error, err error) {
	g, err := secret.Generate(spec.Generate, spec.Bytes, p.Meta.Name)
	if err != nil {
		return nil, err
	}
	secrets[spec.Name] = g.Value
	return func() error {
		if spec.Cert == "" {
			return nil
		}
		path := filepath.Join(p.Dir, spec.Cert)
		if err := os.Rename(path, path+".old"); err != nil && !os.IsNotExist(err) {
			return err
		}
		return atomicfile.Write(path, g.Cert, 0o644)
	}, nil
}

// RotateSecret makes the generated secret name of p anew, one the stack m
// allows, once the person agrees; the next deploy puts it in place.
func RotateSecret(ui UI, p *project.Project, m *manifest.Manifest, name string) error {
	var rotatable []string
	var spec *manifest.Secret
	for i, sec := range m.Secrets {
		if sec.Rotate {
			rotatable = append(rotatable, sec.Name)
		}
		if sec.Name == name {
			spec = &m.Secrets[i]
		}
	}
	switch {
	case spec == nil:
		return fmt.Errorf("%s is not a secret of the stack %s", name, m.Name)
	case !spec.Rotate:
		msg := fmt.Sprintf("the stack %s does not allow making %s anew", m.Name, name)
		if len(rotatable) > 0 {
			msg += "; it allows " + strings.Join(slices.Sorted(slices.Values(rotatable)), ", ")
		}
		return fmt.Errorf("%s", msg)
	}
	ok, err := ui.Confirm(fmt.Sprintf("Make %s of %s anew? The old one stops working once damstack deploy %s puts the new one in place.",
		spec.Name, p.Meta.Name, p.Meta.Name), false)
	if err != nil || !ok {
		return err
	}
	password, err := p.Password()
	if err != nil {
		return err
	}
	secrets, err := p.Secrets(password)
	if err != nil {
		return err
	}
	put, err := MakeAnew(p, *spec, secrets)
	if err != nil {
		return err
	}
	if err := p.SaveSecrets(password, secrets); err != nil {
		return err
	}
	if err := put(); err != nil {
		return err
	}
	fmt.Fprintf(ui.Out(), "%s of %s is new. damstack deploy %s puts it in place; until then the server keeps the old one.\n",
		spec.Name, p.Meta.Name, p.Meta.Name)
	if spec.Cert != "" {
		fmt.Fprintf(ui.Out(), "The certificate is in %s; the old one is next to it, as %s.old.\n", spec.Cert, spec.Cert)
	}
	return nil
}

// SetSecret gives the secret name, one p asks for, the value read gives it,
// or with unset leaves an optional one unset, and keeps it encrypted. It
// returns where the secret comes from.
func SetSecret(p *project.Project, name string, unset bool, read func(manifest.Secret) (string, error)) (SecretSource, error) {
	asked := AskedSecrets(p)
	i := slices.IndexFunc(asked, func(a SecretSource) bool { return a.Spec.Name == name })
	if i < 0 {
		names := make([]string, len(asked))
		for j, a := range asked {
			names[j] = a.Spec.Name
		}
		return SecretSource{}, fmt.Errorf("%s is no secret %s asks for; damstack secret list shows them: %s", name, p.Meta.Name, strings.Join(names, ", "))
	}
	spec := asked[i].Spec
	password, err := p.Password()
	if err != nil {
		return SecretSource{}, err
	}
	secrets, err := p.Secrets(password)
	if err != nil {
		return SecretSource{}, err
	}
	if unset {
		if !spec.Optional {
			return SecretSource{}, fmt.Errorf("%s is needed; it can be replaced, not unset", spec.Name)
		}
		delete(secrets, spec.Name)
	} else {
		value, err := read(spec)
		if err != nil {
			return SecretSource{}, err
		}
		secrets[spec.Name] = value
	}
	if err := p.SaveSecrets(password, secrets); err != nil {
		return SecretSource{}, err
	}
	return asked[i], nil
}

// AccountSet is the sign-ins of an app in a userpass method of the platform's
// Vault, and its users there.
type AccountSet struct {
	App, What, Mount string
	Users            []string
}

// Name is what the set is called among the secrets of a project.
func (a AccountSet) Name() string { return "account:" + a.App + ":" + a.Mount }

// UserRow is the name of one sign-in of a set: each gets its own password,
// so making one anew leaves the others' as they are.
func (a AccountSet) UserRow(user string) string { return a.Name() + ":" + user }

// signIn is the sign-in a name of UserRow stands for.
type signIn struct {
	set  AccountSet
	user string
}

// Secrets are the secrets of a project, where each comes from, and the
// sign-ins of its apps.
type Secrets struct {
	Project  *project.Project
	password string
	// Values are the secrets of vault.yml, by name.
	Values   map[string]any
	Records  project.Records
	Sources  map[string]SecretSource
	Accounts map[string]AccountSet
	// signIns are the sign-ins of the sets with users, by UserRow.
	signIns map[string]signIn
	vault   *vaultClient
	// WireGuard is the secret of the private network's keys, which damstack
	// tunnel rotate makes anew.
	WireGuard string
}

// LoadSecrets reads a project's secrets, where each comes from, and the
// sign-ins of its apps.
func LoadSecrets(ctx context.Context, p *project.Project) (*Secrets, error) {
	m, dir, err := ProjectStack(ctx, p, "")
	if err != nil {
		return nil, err
	}
	v := &Secrets{Project: p, Sources: map[string]SecretSource{}, Accounts: map[string]AccountSet{}, signIns: map[string]signIn{}}
	if v.password, err = p.Password(); err != nil {
		return nil, err
	}
	if v.Values, err = p.Secrets(v.password); err != nil {
		return nil, err
	}
	if v.Records, err = p.Records(); err != nil {
		return nil, err
	}
	for _, s := range m.Secrets {
		v.Sources[s.Name] = SecretSource{Spec: s}
	}
	if m.Server != nil && m.Server.WireGuard != nil {
		v.WireGuard = m.Server.WireGuard.Secret
	}
	config, err := p.Config()
	if err != nil {
		return nil, err
	}
	if env, err := AppEnv(p, m, dir, v.password); err == nil && env["VAULT_ADDR"] != "" {
		v.vault = newVaultClient(env["VAULT_ADDR"], env["VAULT_CACERT"], env["VAULT_TOKEN"])
	}
	for _, ref := range p.Meta.Apps {
		am, _, err := RefStack(ctx, ref)
		if err != nil {
			return nil, err
		}
		for _, s := range am.Secrets {
			v.Sources[s.Name] = SecretSource{Spec: s, App: ref.Name}
		}
		appConfig, _ := config["apps"].(map[string]any)
		data := map[string]any{"config": config, "app": orEmpty(appConfig[ref.Name])}
		for i, a := range am.Accounts {
			mount, err := manifest.RenderData(fmt.Sprintf("accounts[%d].userpass", i), a.Userpass, data, nil)
			if err != nil || strings.TrimSpace(mount) == "" {
				continue
			}
			set := AccountSet{App: ref.Name, What: a.What, Mount: strings.TrimSpace(mount)}
			if v.vault != nil {
				set.Users, _ = v.vault.users(ctx, set.Mount)
			}
			v.Accounts[set.Name()] = set
		}
	}
	for _, set := range v.Accounts {
		if v.vault != nil && len(set.Users) > 0 {
			for _, user := range set.Users {
				v.signIns[set.UserRow(user)] = signIn{set: set, user: user}
			}
		}
	}
	return v, nil
}

func orEmpty(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// HasVault is whether the platform gives the address of its Vault.
func (v *Secrets) HasVault() bool { return v.vault != nil }

// SecretKind is how a secret is made anew.
type SecretKind int

const (
	// Generated: damstack makes a new one.
	Generated SecretKind = iota
	// Asked: the person makes one elsewhere and gives it.
	Asked
	// Accounts: new passwords of an app's sign-ins, set in Vault.
	Accounts
)

// Renewal is a secret to make anew: its name in vault.yml, or a sign-in's
// UserRow, and what it is called for people.
type Renewal struct {
	Name, Label string
	Kind        SecretKind
}

// Password is the new password of a user.
type Password struct{ User, Password string }

// Renewed is how making one secret anew went.
type Renewed struct {
	Name      string
	Err       error
	Passwords []Password
}

// Rotate makes secrets anew: generated ones here, asked ones from values by
// name, an app's sign-ins in Vault; vault.yml is written once, for all of
// them.
func (v *Secrets) Rotate(ctx context.Context, renewals []Renewal, values map[string]string) []Renewed {
	var out []Renewed
	var renewed []int
	changed, puts := false, map[int]func() error{}
	for _, r := range renewals {
		res := Renewed{Name: r.Name}
		switch r.Kind {
		case Accounts:
			in, ok := v.signIns[r.Name]
			if !ok {
				res.Err = fmt.Errorf("%s: no such sign-in", r.Label)
				break
			}
			res.Passwords, res.Err = v.renew(ctx, in.set.Mount, in.user)
			if res.Err == nil {
				renewed = append(renewed, len(out))
			}
		case Asked:
			spec := v.Sources[r.Name].Spec
			if failed := failedCheck(ctx, spec.Checks, values[r.Name]); failed != "" {
				res.Err = fmt.Errorf("%s", failed)
			} else {
				v.Values[r.Name], changed = values[r.Name], true
			}
		default:
			var put func() error
			if put, res.Err = MakeAnew(v.Project, v.Sources[r.Name].Spec, v.Values); res.Err == nil {
				changed = true
				puts[len(out)] = put
			}
		}
		out = append(out, res)
	}
	// Vault has the new passwords already: a record that could not be kept
	// says so, and the passwords can still be copied.
	if len(renewed) > 0 {
		names := make([]string, len(renewed))
		for i, j := range renewed {
			names[i] = out[j].Name
		}
		if err := v.Project.MarkSet(names, time.Now()); err != nil {
			for _, j := range renewed {
				out[j].Err = fmt.Errorf("set in Vault, but not recorded: %w", err)
			}
		}
	}
	if changed {
		if err := v.Project.SaveSecrets(v.password, v.Values); err != nil {
			for i := range out {
				if out[i].Err == nil && out[i].Passwords == nil {
					out[i].Err = err
				}
			}
			return out
		}
		for i, put := range puts {
			out[i].Err = put()
		}
	}
	return out
}

// failedCheck is what the first of the checks of a value that fails says;
// empty when they pass.
func failedCheck(ctx context.Context, specs []manifest.CheckSpec, value any) string {
	if len(specs) == 0 {
		return ""
	}
	for _, r := range checks.Default().Run(ctx, specs, value, nil) {
		if !r.OK {
			return r.Text
		}
	}
	return ""
}

// renew gives one user of a userpass method a new password.
func (v *Secrets) renew(ctx context.Context, mount, user string) ([]Password, error) {
	if v.vault == nil {
		return nil, fmt.Errorf("the platform gives no Vault address")
	}
	g, err := secret.Generate("password", 0, "")
	if err != nil {
		return nil, err
	}
	if err := v.vault.setPassword(ctx, mount, user, g.Value); err != nil {
		return nil, fmt.Errorf("%s: %w", user, err)
	}
	return []Password{{User: user, Password: g.Value}}, nil
}

// vaultClient is the little of Vault's API the secrets need.
type vaultClient struct {
	addr, token string
	http        *http.Client
}

func newVaultClient(addr, caFile, token string) *vaultClient {
	return &vaultClient{addr: strings.TrimRight(addr, "/"), token: token, http: CAClient(caFile)}
}

// CAClient is an HTTP client that trusts the project's CA, the one of the
// platform's private addresses, besides the system's.
func CAClient(caFile string) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if pem, err := os.ReadFile(caFile); err == nil {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		pool.AppendCertsFromPEM(pem)
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}
}

func (c *vaultClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var r *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(data)
	} else {
		r = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.addr+"/v1/"+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", c.token)
	return c.http.Do(req)
}

// users are the users of a userpass method; none is an empty list, and nil
// with an error when Vault did not answer.
func (c *vaultClient) users(ctx context.Context, mount string) ([]string, error) {
	resp, err := c.do(ctx, "LIST", "auth/"+url.PathEscape(mount)+"/users", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return []string{}, nil
	case http.StatusOK:
	default:
		return nil, fmt.Errorf("vault: %s", resp.Status)
	}
	var out struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return append([]string{}, out.Data.Keys...), nil
}

func (c *vaultClient) setPassword(ctx context.Context, mount, user, password string) error {
	resp, err := c.do(ctx, http.MethodPost, "auth/"+url.PathEscape(mount)+"/users/"+url.PathEscape(user)+"/password",
		map[string]string{"password": password})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("vault: %s", resp.Status)
	}
	return nil
}
