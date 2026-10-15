package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/eugene-panin/damstack/internal/ops"
	"github.com/eugene-panin/damstack/internal/project"
)

func restoreCommand(s *cli) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "restore [project]",
		Short: "Set up a new server for a project from its latest backup on this machine, in place of a lost one",
		Long: "Sets the server up as a deploy does, then puts back what the backup holds: the stack says what, such as\n" +
			"the data of Consul and Vault and of the apps. Give stack.yaml the address of the new server first, with\n" +
			"damstack edit. Whatever is on that server is replaced; a server that is still there needs no restore.",
		Example: "  damstack backup status ovh\n" +
			"  damstack edit ovh\n" +
			"  damstack restore ovh",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := pickProject(s, firstArg(args))
			if err != nil {
				return err
			}
			return restore(cmd.Context(), s, p, yes)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "go ahead without asking; needed without a terminal")
	return cmd
}

func restore(ctx context.Context, s *cli, p *project.Project, yes bool) error {
	m, dir, err := ops.ProjectStack(ctx, p, "")
	if err != nil {
		return err
	}
	r, when, err := ops.Restore(ctx, deployer(s, ops.Options{Yes: yes}, false), p, m, dir)
	if err != nil {
		return err
	}
	if err := deployed(ctx, s, yes, p, m, dir, r); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "\n%s is back from the backup of %s.\n", p.Meta.Name, when)
	return nil
}
