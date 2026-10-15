package main

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/colorprofile"

	"github.com/eugene-panin/damstack/internal/ops"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/ui/tui"
)

// secretsView is what the secrets screen lists of a project.
type secretsView struct {
	*ops.Secrets
	rows []tui.SecretRow
}

// loadSecrets reads a project's secrets, where each comes from, and the
// sign-ins of its apps, for the screen.
func loadSecrets(ctx context.Context, p *project.Project) (*secretsView, error) {
	secrets, err := ops.LoadSecrets(ctx, p)
	if err != nil {
		return nil, err
	}
	v := &secretsView{Secrets: secrets}
	v.rows = v.list()
	return v, nil
}

// list is the rows of the screen: every secret of vault.yml, and each app's
// sign-ins.
func (v *secretsView) list() []tui.SecretRow {
	var rows []tui.SecretRow
	for _, name := range slices.Sorted(maps.Keys(v.Values)) {
		src, known := v.Sources[name]
		row := tui.SecretRow{Name: name, Label: label(name), Owner: orDefault(src.App, "platform"), Takes: "damstack deploy"}
		switch {
		case name == v.WireGuard:
			row.Keep = "damstack tunnel rotate gives new keys"
		case !known:
			row.Keep = "a step of the stack keeps it"
		case src.Spec.Ask != "":
			row.Kind, row.NewAt = tui.Asked, src.Spec.NewAt
		case src.Spec.Keep != "":
			row.Keep = src.Spec.Keep
		case !src.Spec.Rotate:
			row.Keep = "the stack does not make it anew yet"
		}
		if src.Spec.Env != "" {
			row.Takes = "damstack deploy restarts " + servicesOf(src.Spec, src.App)
		}
		v.stamp(&row)
		rows = append(rows, row)
	}
	for _, set := range v.Accounts {
		// A row per user: one person's password is made anew, not everyone's.
		if v.HasVault() && len(set.Users) > 0 {
			for _, user := range set.Users {
				row := tui.SecretRow{Name: set.UserRow(user), Label: set.App + " " + set.What + ": " + user,
					Owner: set.App, Kind: tui.Accounts, Takes: "no restart: Vault checks it at sign-in"}
				v.stamp(&row)
				rows = append(rows, row)
			}
			continue
		}
		row := tui.SecretRow{Name: set.Name(), Label: set.App + " " + set.What, Owner: set.App, Kind: tui.Accounts,
			Takes: "no restart: Vault checks it at sign-in"}
		switch {
		case !v.HasVault():
			row.Keep = "the platform gives no Vault address"
		case set.Users == nil:
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
	rec := v.Records.Secrets[row.Name]
	row.Set, row.Seen = rec.Set, !rec.Seen.IsZero()
	if row.Set.IsZero() {
		row.Set = v.Project.Meta.Created
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

// kinds are the kinds of the screen's rows, as ops makes them anew.
var kinds = map[tui.SecretKind]ops.SecretKind{tui.Generated: ops.Generated, tui.Asked: ops.Asked, tui.Accounts: ops.Accounts}

// rotate makes rows anew: generated secrets here, asked ones from values, an
// app's sign-ins in Vault; vault.yml is written once, for all of them.
func (v *secretsView) rotate(ctx context.Context, rows []tui.SecretRow, values map[string]string) []tui.Rotated {
	renewals := make([]ops.Renewal, len(rows))
	for i, r := range rows {
		renewals[i] = ops.Renewal{Name: r.Name, Label: r.Label, Kind: kinds[r.Kind]}
	}
	var out []tui.Rotated
	for _, r := range v.Rotate(ctx, renewals, values) {
		res := tui.Rotated{Name: r.Name, Err: r.Err}
		for _, pw := range r.Passwords {
			res.Passwords = append(res.Passwords, tui.Password(pw))
		}
		out = append(out, res)
	}
	return out
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
	model := tui.NewSecrets(p.Meta.Name, v.rows, v.Records.OldAfterDays(), time.Now(), tui.SecretActions{
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
	m, dir, err := ops.ProjectStack(ctx, p, "")
	if err != nil {
		return err
	}
	var apps []string
	for _, r := range rows {
		if r.Kind == tui.Accounts {
			continue // set in Vault already
		}
		app := v.Sources[r.Name].App
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
		case time.Since(r.Set) > time.Duration(v.Records.OldAfterDays())*24*time.Hour:
			why = fmt.Sprintf("older than %d days", v.Records.OldAfterDays())
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Name, r.Owner, r.Set.Format(time.DateOnly), why)
	}
	return tw.Flush()
}
