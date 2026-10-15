package main

import (
	"github.com/spf13/cobra"

	"github.com/eugene-panin/damstack/internal/ops"
)

func backupCommand(s *streams) *cobra.Command {
	status := func(cmd *cobra.Command, args []string) error {
		p, err := pickProject(s, firstArg(args))
		if err != nil {
			return err
		}
		return ops.BackupStatus(cmd.Context(), streamsUI{s}, p)
	}
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "The backups of a project: the server makes them every night, this machine pulls them",
		Args:  cobra.NoArgs,
		RunE:  status,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "status [project]",
		Short: "Show the backups on this machine and when they were last pulled",
		Args:  cobra.MaximumNArgs(1),
		RunE:  status,
	})
	var scheduled bool
	pull := &cobra.Command{
		Use:   "pull [project]",
		Short: "Copy the new backups from the server to this machine, then drop the old ones as stack.yaml says",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := pickProject(s, firstArg(args))
			if err != nil {
				return err
			}
			return ops.PullBackup(cmd.Context(), streamsUI{s}, p, scheduled)
		},
	}
	pull.Flags().BoolVar(&scheduled, "scheduled", false, "run as the scheduled pull: skip quietly when the server does not answer")
	pull.Flags().MarkHidden("scheduled")
	cmd.AddCommand(pull)
	cmd.AddCommand(&cobra.Command{
		Use:   "schedule [project]",
		Short: "Pull the backups by themselves, as often as stack.yaml says",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := pickProject(s, firstArg(args))
			if err != nil {
				return err
			}
			b, err := ops.LoadBackup(cmd.Context(), p)
			if err != nil {
				return err
			}
			return ops.ScheduleBackup(cmd.Context(), streamsUI{s}, p, b, true)
		},
	})
	var to string
	kitCmd := &cobra.Command{
		Use:   "kit [project]",
		Short: "Pack the project and its vault password into one encrypted file, to bring it back on another computer",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := pickProject(s, firstArg(args))
			if err != nil {
				return err
			}
			return ops.MakeKit(streamsUI{s}, p, to)
		},
	}
	kitCmd.Flags().StringVar(&to, "to", "", "the directory to write the kit to, the current one by default")
	cmd.AddCommand(kitCmd)
	var into string
	open := &cobra.Command{
		Use:   "open <kit>",
		Short: "Bring a project back on this computer from its recovery kit",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			passphrase := func() (string, error) { return s.prompt.Secret("The passphrase of the kit") }
			return ops.OpenKit(cmd.Context(), streamsUI{s}, args[0], into, passphrase)
		},
	}
	open.Flags().StringVar(&into, "dir", "", "where the project goes, ~/.damstack/<project> by default")
	cmd.AddCommand(open)
	return cmd
}
