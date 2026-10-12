package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"

	"github.com/eugene-panin/damstack/internal/ask"
	"github.com/eugene-panin/damstack/internal/checks"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/ui/tui"
)

func appKeysCommand(s *streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keys [app]",
		Short: "The keys an app asks for, such as the API keys of an LLM: which are set, and setting one; a full screen on a terminal",
		Example: "  damstack app keys sauron\n" +
			"  damstack app keys sauron --plain",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := pickProject(s, "")
			if err != nil {
				return err
			}
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return appKeys(cmd.Context(), s, p, name)
		},
	}
	cmd.Flags().BoolVar(&s.plain, "plain", false, "list them as lines instead of the full-screen view on a terminal")
	return cmd
}

// appKeys shows the keys of an app, the only one that asks for any when
// name is empty, and sets the ones the person gives; asked, it deploys after.
func appKeys(ctx context.Context, s *streams, p *project.Project, name string) error {
	asked := askedSecrets(p)
	if name == "" {
		var apps []string
		for _, a := range asked {
			if a.app != "" && !slices.Contains(apps, a.app) {
				apps = append(apps, a.app)
			}
		}
		if len(apps) != 1 {
			return fmt.Errorf("name the app: damstack app keys <app>; apps asking for keys: %s", orDefault(strings.Join(apps, ", "), "none"))
		}
		name = apps[0]
	}
	ref, ok := p.Meta.App(name)
	if !ok {
		return fmt.Errorf("%s is no app of %s; damstack app list shows them", name, p.Meta.Name)
	}
	am, _, err := refStack(ctx, ref)
	if err != nil {
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
	records, err := p.Records()
	if err != nil {
		return err
	}
	deployed := lastStepDone(p, func(step string) bool { return strings.HasPrefix(step, name+"/") })
	var keys []tui.Key
	made := 0
	specs := map[string]manifest.Secret{}
	for _, sec := range am.Secrets {
		if sec.Ask == "" {
			if _, ok := secrets[sec.Name]; ok {
				made++
			}
			continue
		}
		specs[sec.Name] = sec
		v, _ := secrets[sec.Name].(string)
		rec := records.Secrets[sec.Name]
		keys = append(keys, tui.Key{Name: sec.Name, Label: keyLabel(sec.Ask), UsedBy: sec.For, Set: v != "",
			Changed: rec.Set, Pending: rec.Set.After(deployed), Optional: sec.Optional, NewAt: sec.NewAt})
	}
	if !s.screen() {
		tw := tabwriter.NewWriter(s.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "KEY\tSTATE\tUSED BY\tWHAT")
		for _, k := range keys {
			state := "not set"
			if k.Set {
				state = "set"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", k.Name, state, orDefault(strings.Join(k.UsedBy, " "), "every service"), specs[k.Name].Ask)
		}
		fmt.Fprintf(tw, "\ndamstack secret set <key> sets one, damstack deploy %s puts it in place.\n", p.Meta.Name)
		return tw.Flush()
	}

	save := func(change func(map[string]any)) error {
		current, err := p.Secrets(password)
		if err != nil {
			return err
		}
		change(current)
		return p.SaveSecrets(password, current)
	}
	tui.UseProfile(colorprofile.Detect(s.out, os.Environ()))
	source := strings.TrimPrefix(strings.TrimPrefix(ref.URL, "https://"), "github.com/")
	model := tui.NewAppKeys(name, source+" · "+ref.Tag, keys, made, time.Now(), tui.KeyActions{
		Set: func(key, value string) error {
			check := func(specs []manifest.CheckSpec, v any, answers map[string]any) []checks.Result {
				return checks.Default().Run(ctx, specs, v, answers)
			}
			if failed := ask.Report(nil, check, specs[key].Checks, value, nil); failed != "" {
				return fmt.Errorf("%s: %s", keyLabel(specs[key].Ask), failed)
			}
			return save(func(m map[string]any) { m[key] = value })
		},
		Unset: func(key string) error {
			return save(func(m map[string]any) { delete(m, key) })
		},
		Open: openURL,
	})
	if _, err := tea.NewProgram(model, tea.WithInput(s.in), tea.WithOutput(s.out), tea.WithContext(ctx)).Run(); err != nil {
		return err
	}
	if !model.Deploy() {
		return nil
	}
	args := []string{"deploy", p.Meta.Name}
	if !platformPending(p, password) {
		args = append(args, "--apps-only")
	}
	deploy := exec.CommandContext(ctx, self(), args...)
	deploy.Stdin, deploy.Stdout, deploy.Stderr = os.Stdin, os.Stdout, os.Stderr
	return deploy.Run()
}

// keyLabel is the name of a key in what an app asks for it with: "xAI API
// key (Grok), the default reader" is xAI API key.
func keyLabel(ask string) string {
	if i := strings.IndexAny(ask, "(,"); i > 0 {
		ask = ask[:i]
	}
	return strings.TrimSpace(ask)
}

// lastStepDone is when a deploy last finished a step that is, by its name, one
// of those asked for.
func lastStepDone(p *project.Project, of func(step string) bool) time.Time {
	entries, _ := p.History()
	var last time.Time
	for _, e := range entries {
		if e.Command == "deploy" && e.Result == project.OK && of(e.Step) && e.Time.After(last) {
			last = e.Time
		}
	}
	return last
}

// platformPending is whether a secret of the platform changed since its
// steps last ran: a deploy of the apps only would not put it in place.
func platformPending(p *project.Project, password string) bool {
	records, err := p.Records()
	if err != nil {
		return true
	}
	apps := appSecretNames(p)
	deployed := lastStepDone(p, func(step string) bool { return !strings.Contains(step, "/") })
	for name, rec := range records.Secrets {
		if !apps[name] && !strings.HasPrefix(name, "account:") && rec.Set.After(deployed) {
			return true
		}
	}
	return false
}
