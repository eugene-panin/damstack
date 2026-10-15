package main

import (
	"bufio"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/eugene-panin/damstack/internal/ask"
	"github.com/eugene-panin/damstack/internal/checks"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/ops"
)

func secretCommand(s *streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "The secrets of a project: those damstack generates, and those you give it, such as API keys",
		Long: "The secrets of a project: those damstack generates, those you give it, such as API keys, and the " +
			"sign-ins of its apps. Alone, it shows them all, the riskiest first, and makes the ones you pick anew; " +
			"on a pipe or with --plain, it lists them.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := pickProject(s, "")
			if err != nil {
				return err
			}
			return secretsScreen(cmd.Context(), s, p)
		},
	}
	cmd.Flags().BoolVar(&s.plain, "plain", false, "list them as lines instead of the full-screen view on a terminal")
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
			m, _, err := ops.ProjectStack(cmd.Context(), p, "")
			if err != nil {
				return err
			}
			return ops.RotateSecret(streamsUI{s}, p, m, args[0])
		},
	}
	rotate.Flags().BoolVar(&s.yes, "yes", false, "go ahead without asking; needed without a terminal")
	cmd.AddCommand(rotate, secretSetCommand(s), secretListCommand(s))
	return cmd
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
			a, err := ops.SetSecret(p, args[0], unset, func(spec manifest.Secret) (string, error) {
				value, err := readSecret(s, spec)
				if err != nil {
					return "", err
				}
				check := func(specs []manifest.CheckSpec, v any, answers map[string]any) []checks.Result {
					return checks.Default().Run(cmd.Context(), specs, v, answers)
				}
				if failed := ask.Report(s.prompt, check, spec.Checks, value, nil); failed != "" {
					return "", fmt.Errorf("%s: %s", spec.Name, failed)
				}
				return value, nil
			})
			if err != nil {
				return err
			}
			spec := a.Spec
			what := "set"
			if unset {
				what = "unset"
			}
			fmt.Fprintf(s.out, "%s of %s is %s, kept encrypted. damstack deploy %s puts it in place", spec.Name, p.Meta.Name, what, p.Meta.Name)
			if spec.Env != "" {
				fmt.Fprintf(s.out, ": %s restarts with it", servicesOf(spec, a.App))
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
			for _, a := range ops.AskedSecrets(p) {
				state := "set"
				if v, _ := secrets[a.Spec.Name].(string); v == "" {
					switch {
					case a.Spec.When != "":
						state = "only if " + a.Spec.When
					case a.Spec.Optional:
						state = "not set (optional)"
					default:
						state = "not set"
					}
				}
				by := "the platform"
				if a.App != "" {
					by = a.App
					if a.Spec.Env != "" {
						by = servicesOf(a.Spec, a.App) + " as " + a.Spec.Env
					}
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.Spec.Name, state, by, a.Spec.Ask)
			}
			return tw.Flush()
		},
	}
}
