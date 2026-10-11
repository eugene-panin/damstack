package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/eugene-panin/damstack/internal/ask"
	"github.com/eugene-panin/damstack/internal/checks"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/secret"
)

func secretCommand(s *streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "The secrets of a project: those damstack generates, and those you give it, such as API keys",
	}
	rotate := &cobra.Command{
		Use:   "rotate <secret>",
		Short: "Make a generated secret anew, one the stack allows; the next deploy puts it in place",
		Example: "  damstack secret rotate vault_stack_ca_key -p ovh\n" +
			"  damstack deploy ovh",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := pickProject(s, "")
			if err != nil {
				return err
			}
			m, _, err := projectStack(cmd.Context(), p, "")
			if err != nil {
				return err
			}
			var rotatable []string
			var spec *manifest.Secret
			for i, sec := range m.Secrets {
				if sec.Rotate {
					rotatable = append(rotatable, sec.Name)
				}
				if sec.Name == args[0] {
					spec = &m.Secrets[i]
				}
			}
			switch {
			case spec == nil:
				return fmt.Errorf("%s is not a secret of the stack %s", args[0], m.Name)
			case !spec.Rotate:
				msg := fmt.Sprintf("the stack %s does not allow making %s anew", m.Name, args[0])
				if len(rotatable) > 0 {
					msg += "; it allows " + strings.Join(slices.Sorted(slices.Values(rotatable)), ", ")
				}
				return fmt.Errorf("%s", msg)
			}
			ok, err := s.confirm(fmt.Sprintf("Make %s of %s anew? The old one stops working once damstack deploy %s puts the new one in place.",
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
			g, err := secret.Generate(spec.Generate, spec.Bytes, p.Meta.Name)
			if err != nil {
				return err
			}
			if spec.Cert != "" {
				path := filepath.Join(p.Dir, spec.Cert)
				if err := os.Rename(path, path+".old"); err != nil && !os.IsNotExist(err) {
					return err
				}
				if err := os.WriteFile(path, g.Cert, 0o644); err != nil {
					return err
				}
			}
			secrets[spec.Name] = g.Value
			if err := p.SaveSecrets(password, secrets); err != nil {
				return err
			}
			fmt.Fprintf(s.out, "%s of %s is new. damstack deploy %s puts it in place; until then the server keeps the old one.\n",
				spec.Name, p.Meta.Name, p.Meta.Name)
			if spec.Cert != "" {
				fmt.Fprintf(s.out, "The certificate is in %s; the old one is next to it, as %s.old.\n", spec.Cert, spec.Cert)
			}
			return nil
		},
	}
	rotate.Flags().BoolVar(&s.yes, "yes", false, "go ahead without asking; needed without a terminal")
	cmd.AddCommand(rotate, secretSetCommand(s), secretListCommand(s))
	return cmd
}

// askedSecret is an asked secret of a project's platform or one of its apps,
// and the app it is of ("" for the platform).
type askedSecret struct {
	spec manifest.Secret
	app  string
}

// askedSecrets are the secrets a person gives a project: those its platform and
// its apps ask for.
func askedSecrets(p *project.Project) []askedSecret {
	var out []askedSecret
	if m, _ := cachedStack(p); m != nil {
		for _, sec := range m.Secrets {
			if sec.Ask != "" {
				out = append(out, askedSecret{sec, ""})
			}
		}
	}
	for _, ref := range p.Meta.Apps {
		if am, _ := cachedRef(ref); am != nil {
			for _, sec := range am.Secrets {
				if sec.Ask != "" {
					out = append(out, askedSecret{sec, ref.Name})
				}
			}
		}
	}
	return out
}

func secretSetCommand(s *streams) *cobra.Command {
	var unset bool
	cmd := &cobra.Command{
		Use:   "set <secret>",
		Short: "Give a secret the platform or an app asks for, such as an API key; the next deploy puts it in place",
		Example: "  damstack secret set sauron_xai_api_key -p twinmind\n" +
			"  printf %s \"$KEY\" | damstack secret set sauron_xai_api_key -p twinmind\n" +
			"  damstack secret set sauron_xai_api_key --unset",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := pickProject(s, "")
			if err != nil {
				return err
			}
			asked := askedSecrets(p)
			i := slices.IndexFunc(asked, func(a askedSecret) bool { return a.spec.Name == args[0] })
			if i < 0 {
				names := make([]string, len(asked))
				for j, a := range asked {
					names[j] = a.spec.Name
				}
				return fmt.Errorf("%s is no secret %s asks for; damstack secret list shows them: %s", args[0], p.Meta.Name, strings.Join(names, ", "))
			}
			spec := asked[i].spec
			password, err := p.Password()
			if err != nil {
				return err
			}
			secrets, err := p.Secrets(password)
			if err != nil {
				return err
			}
			if unset {
				if !spec.Optional {
					return fmt.Errorf("%s is needed; it can be replaced, not unset", spec.Name)
				}
				delete(secrets, spec.Name)
			} else {
				value, err := readSecret(s, spec)
				if err != nil {
					return err
				}
				check := func(specs []manifest.CheckSpec, v any, answers map[string]any) []checks.Result {
					return checks.Default().Run(cmd.Context(), specs, v, answers)
				}
				if failed := ask.Report(s.prompt, check, spec.Checks, value, nil); failed != "" {
					return fmt.Errorf("%s: %s", spec.Name, failed)
				}
				secrets[spec.Name] = value
			}
			if err := p.SaveSecrets(password, secrets); err != nil {
				return err
			}
			what := "set"
			if unset {
				what = "unset"
			}
			fmt.Fprintf(s.out, "%s of %s is %s, kept encrypted. damstack deploy %s puts it in place", spec.Name, p.Meta.Name, what, p.Meta.Name)
			if spec.Env != "" {
				fmt.Fprintf(s.out, ": %s restarts with it", servicesOf(spec, asked[i].app))
			}
			fmt.Fprintln(s.out, ".")
			return nil
		},
	}
	cmd.Flags().BoolVar(&unset, "unset", false, "leave an optional secret unset")
	return cmd
}

// readSecret reads a secret's value: hidden from the terminal, or the first
// line of stdin when it is not one.
func readSecret(s *streams, spec manifest.Secret) (string, error) {
	if s.prompt.NoTerminal {
		line, err := bufio.NewReader(s.in).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("%s: give its value on stdin, or run this on a terminal", spec.Name)
		}
		return strings.TrimSpace(line), nil
	}
	value, err := s.prompt.Secret(spec.Ask)
	if err == nil && value == "" {
		err = fmt.Errorf("%s: no value given", spec.Name)
	}
	return value, err
}

// servicesOf says which services of an app take a secret.
func servicesOf(spec manifest.Secret, app string) string {
	if len(spec.For) == 0 {
		return "every service of " + app
	}
	return app + ": " + strings.Join(spec.For, ", ")
}

func secretListCommand(s *streams) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "The secrets the platform and the apps ask for: which are set, and which services take them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := pickProject(s, "")
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
			tw := tabwriter.NewWriter(s.out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "SECRET\tSTATE\tTAKEN BY\tWHAT")
			for _, a := range askedSecrets(p) {
				state := "set"
				if v, _ := secrets[a.spec.Name].(string); v == "" {
					switch {
					case a.spec.When != "":
						state = "only if " + a.spec.When
					case a.spec.Optional:
						state = "not set (optional)"
					default:
						state = "not set"
					}
				}
				by := "the platform"
				if a.app != "" {
					by = a.app
					if a.spec.Env != "" {
						by = servicesOf(a.spec, a.app) + " as " + a.spec.Env
					}
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.spec.Name, state, by, a.spec.Ask)
			}
			return tw.Flush()
		},
	}
}
