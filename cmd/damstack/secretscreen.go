package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/colorprofile"

	"github.com/eugene-panin/damstack/internal/ask"
	"github.com/eugene-panin/damstack/internal/checks"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/secret"
	"github.com/eugene-panin/damstack/internal/ui/tui"
)

// secretSource is where a secret of vault.yml comes from: the platform's
// stack or an app's, and its spec there; spec is zero for one a step of the
// stack keeps, such as Vault's root token.
type secretSource struct {
	spec manifest.Secret
	app  string
}

// accountSet is the sign-ins of an app in a userpass method of the platform's
// Vault, and its users there.
type accountSet struct {
	app, what, mount string
	users            []string
}

func (a accountSet) name() string { return "account:" + a.app + ":" + a.mount }

// userRow is the row of one sign-in of a set: each gets its own password,
// so making one anew leaves the others' as they are.
func (a accountSet) userRow(user string) string { return a.name() + ":" + user }

// signIn is the sign-in a row of the screen stands for.
type signIn struct {
	set  accountSet
	user string
}

// secretsView is what the secrets screen lists of a project.
type secretsView struct {
	p        *project.Project
	password string
	secrets  map[string]any
	records  project.Records
	sources  map[string]secretSource
	accounts map[string]accountSet
	// signIns are the account rows, by name: one per user of a set.
	signIns map[string]signIn
	vault   *vaultClient
	rows    []tui.SecretRow
	// wireguard is the secret of the private network's keys, which damstack
	// tunnel rotate makes anew.
	wireguard string
}

// loadSecrets reads a project's secrets, where each comes from, and the
// sign-ins of its apps, for the screen.
func loadSecrets(ctx context.Context, p *project.Project) (*secretsView, error) {
	m, dir, err := projectStack(ctx, p, "")
	if err != nil {
		return nil, err
	}
	v := &secretsView{p: p, sources: map[string]secretSource{}, accounts: map[string]accountSet{}, signIns: map[string]signIn{}}
	if v.password, err = p.Password(); err != nil {
		return nil, err
	}
	if v.secrets, err = p.Secrets(v.password); err != nil {
		return nil, err
	}
	if v.records, err = p.Records(); err != nil {
		return nil, err
	}
	for _, s := range m.Secrets {
		v.sources[s.Name] = secretSource{spec: s}
	}
	if m.Server != nil && m.Server.WireGuard != nil {
		v.wireguard = m.Server.WireGuard.Secret
	}
	config, err := p.Config()
	if err != nil {
		return nil, err
	}
	if env, err := appEnv(p, m, dir, v.password); err == nil && env["VAULT_ADDR"] != "" {
		v.vault = newVaultClient(env["VAULT_ADDR"], env["VAULT_CACERT"], env["VAULT_TOKEN"])
	}
	for _, ref := range p.Meta.Apps {
		am, _, err := refStack(ctx, ref)
		if err != nil {
			return nil, err
		}
		for _, s := range am.Secrets {
			v.sources[s.Name] = secretSource{spec: s, app: ref.Name}
		}
		appConfig, _ := config["apps"].(map[string]any)
		data := map[string]any{"config": config, "app": orEmpty(appConfig[ref.Name])}
		for i, a := range am.Accounts {
			mount, err := manifest.RenderData(fmt.Sprintf("accounts[%d].userpass", i), a.Userpass, data, nil)
			if err != nil || strings.TrimSpace(mount) == "" {
				continue
			}
			set := accountSet{app: ref.Name, what: a.What, mount: strings.TrimSpace(mount)}
			if v.vault != nil {
				set.users, _ = v.vault.users(ctx, set.mount)
			}
			v.accounts[set.name()] = set
		}
	}
	v.rows = v.list()
	return v, nil
}

func orEmpty(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// list is the rows of the screen: every secret of vault.yml, and each app's
// sign-ins.
func (v *secretsView) list() []tui.SecretRow {
	var rows []tui.SecretRow
	for _, name := range slices.Sorted(maps.Keys(v.secrets)) {
		src, known := v.sources[name]
		row := tui.SecretRow{Name: name, Label: label(name), Owner: orDefault(src.app, "platform"), Takes: "damstack deploy"}
		switch {
		case name == v.wireguard:
			row.Keep = "damstack tunnel rotate gives new keys"
		case !known:
			row.Keep = "a step of the stack keeps it"
		case src.spec.Ask != "":
			row.Kind, row.NewAt = tui.Asked, src.spec.NewAt
		case src.spec.Keep != "":
			row.Keep = src.spec.Keep
		case !src.spec.Rotate:
			row.Keep = "the stack does not make it anew yet"
		}
		if src.spec.Env != "" {
			row.Takes = "damstack deploy restarts " + servicesOf(src.spec, src.app)
		}
		v.stamp(&row)
		rows = append(rows, row)
	}
	for _, set := range v.accounts {
		// A row per user: one person's password is made anew, not everyone's.
		if v.vault != nil && len(set.users) > 0 {
			for _, user := range set.users {
				row := tui.SecretRow{Name: set.userRow(user), Label: set.app + " " + set.what + ": " + user,
					Owner: set.app, Kind: tui.Accounts, Takes: "no restart: Vault checks it at sign-in"}
				v.signIns[row.Name] = signIn{set: set, user: user}
				v.stamp(&row)
				rows = append(rows, row)
			}
			continue
		}
		row := tui.SecretRow{Name: set.name(), Label: set.app + " " + set.what, Owner: set.app, Kind: tui.Accounts,
			Takes: "no restart: Vault checks it at sign-in"}
		switch {
		case v.vault == nil:
			row.Keep = "the platform gives no Vault address"
		case set.users == nil:
			row.Keep = "Vault did not answer: is the tunnel on?"
		default:
			row.Keep = "no users yet: add them in Vault"
		}
		v.stamp(&row)
		rows = append(rows, row)
	}
	return rows
}

// stamp gives a row what the records say: when it was set (when the project
// was made, before damstack kept records), and whether it was seen.
func (v *secretsView) stamp(row *tui.SecretRow) {
	rec := v.records.Secrets[row.Name]
	row.Set, row.Seen = rec.Set, !rec.Seen.IsZero()
	if row.Set.IsZero() {
		row.Set = v.p.Meta.Created
	}
}

// label is a secret's name as the screen says it: vault_consul_acl_agent_token
// is consul acl agent token.
func label(name string) string {
	return strings.ReplaceAll(strings.TrimPrefix(name, "vault_"), "_", " ")
}

func orDefault(s, otherwise string) string {
	if s == "" {
		return otherwise
	}
	return s
}

// rotate makes rows anew: generated secrets here, asked ones from values, an
// app's sign-ins in Vault; vault.yml is written once, for all of them.
func (v *secretsView) rotate(ctx context.Context, rows []tui.SecretRow, values map[string]string) []tui.Rotated {
	var out []tui.Rotated
	var renewed []int
	changed, puts := false, map[int]func() error{}
	for _, r := range rows {
		res := tui.Rotated{Name: r.Name}
		switch r.Kind {
		case tui.Accounts:
			in, ok := v.signIns[r.Name]
			if !ok {
				res.Err = fmt.Errorf("%s: no such sign-in", r.Label)
				break
			}
			res.Passwords, res.Err = v.renew(ctx, in.set.mount, in.user)
			if res.Err == nil {
				renewed = append(renewed, len(out))
			}
		case tui.Asked:
			spec := v.sources[r.Name].spec
			check := func(specs []manifest.CheckSpec, val any, answers map[string]any) []checks.Result {
				return checks.Default().Run(ctx, specs, val, answers)
			}
			if failed := ask.Report(nil, check, spec.Checks, values[r.Name], nil); failed != "" {
				res.Err = fmt.Errorf("%s", failed)
			} else {
				v.secrets[r.Name], changed = values[r.Name], true
			}
		default:
			var put func() error
			if put, res.Err = makeAnew(v.p, v.sources[r.Name].spec, v.secrets); res.Err == nil {
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
		if err := v.p.MarkSet(names, time.Now()); err != nil {
			for _, j := range renewed {
				out[j].Err = fmt.Errorf("set in Vault, but not recorded: %w", err)
			}
		}
	}
	if changed {
		if err := v.p.SaveSecrets(v.password, v.secrets); err != nil {
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

// renew gives one user of a userpass method a new password.
func (v *secretsView) renew(ctx context.Context, mount, user string) ([]tui.Password, error) {
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
	return []tui.Password{{User: user, Password: g.Value}}, nil
}

// secretsScreen shows a project's secrets, and makes the ones the person
// picks anew; asked, it deploys after.
func secretsScreen(ctx context.Context, s *streams, p *project.Project) error {
	v, err := loadSecrets(ctx, p)
	if err != nil {
		return err
	}
	if !s.screen() {
		return v.table(s)
	}
	tui.UseProfile(colorprofile.Detect(s.out, os.Environ()))
	model := tui.NewSecrets(p.Meta.Name, v.rows, v.records.OldAfterDays(), time.Now(), tui.SecretActions{
		Rotate: func(rows []tui.SecretRow, values map[string]string) []tui.Rotated {
			return v.rotate(ctx, rows, values)
		},
		MarkSeen: func(name string) error {
			return p.MarkSeen(name, "marked on the secrets screen", time.Now())
		},
		OldAfter: func(days int) error {
			r, err := p.Records()
			if err != nil {
				return err
			}
			r.OldAfter = days
			return p.SaveRecords(r)
		},
		Open: openURL,
		Copy: copyText,
	})
	if err := s.show(ctx, model); err != nil {
		return err
	}
	if !model.Deploy() {
		return nil
	}
	return deployRotated(ctx, s, p, v, model.Rotated())
}

// deployRotated puts secrets made anew in place, here on the same terminal:
// only the apps whose secrets they are; a platform's secret opens the pick of
// what to deploy, as d on the home screen does.
func deployRotated(ctx context.Context, s *streams, p *project.Project, v *secretsView, rows []tui.SecretRow) error {
	m, dir, err := projectStack(ctx, p, "")
	if err != nil {
		return err
	}
	var apps []string
	for _, r := range rows {
		if r.Kind == tui.Accounts {
			continue // set in Vault already
		}
		app := v.sources[r.Name].app
		if app == "" {
			return homeAction(ctx, s, p, "d")
		}
		if !slices.Contains(apps, app) {
			apps = append(apps, app)
		}
	}
	if len(apps) == 0 {
		return nil
	}
	return deployParts(ctx, s, p, m, dir, apps...)
}

// table is the screen's list as lines, for a pipe or --plain.
func (v *secretsView) table(s *streams) error {
	tw := tabwriter.NewWriter(s.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SECRET\tOWNER\tSET\tWHY")
	for _, r := range v.rows {
		why := "-"
		switch {
		case r.Seen:
			why = "seen in plain text"
		case r.Keep != "":
			why = "can't rotate: " + r.Keep
		case time.Since(r.Set) > time.Duration(v.records.OldAfterDays())*24*time.Hour:
			why = fmt.Sprintf("older than %d days", v.records.OldAfterDays())
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Name, r.Owner, r.Set.Format(time.DateOnly), why)
	}
	return tw.Flush()
}

// vaultClient is the little of Vault's API the secrets screen needs.
type vaultClient struct {
	addr, token string
	http        *http.Client
}

func newVaultClient(addr, caFile, token string) *vaultClient {
	return &vaultClient{addr: strings.TrimRight(addr, "/"), token: token, http: caClient(caFile)}
}

// caClient is an HTTP client that trusts the project's CA, the one of the
// platform's private addresses, besides the system's.
func caClient(caFile string) *http.Client {
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
