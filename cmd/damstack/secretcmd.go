package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/secret"
)

func secretCommand(s *streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "The secrets damstack generated for a project",
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
	cmd.AddCommand(rotate)
	return cmd
}
