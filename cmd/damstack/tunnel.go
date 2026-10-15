package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/spf13/cobra"

	"github.com/eugene-panin/damstack/internal/ops"
)

var deviceRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

func tunnelCommand(s *cli) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tunnel",
		Short: "The devices on the private network of the server, and their WireGuard keys",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list [project]",
		Short: "List the devices: their addresses, how old their keys are, when each last connected",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := openTunnel(cmd.Context(), s, firstArg(args), false)
			if err != nil {
				return err
			}
			return t.List(cmd.Context())
		},
	})
	var qr, print bool
	show := &cobra.Command{
		Use:   "show <device>",
		Short: "Give a device its configuration: into the WireGuard app of this Mac, or as a QR code for a phone",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := openTunnel(cmd.Context(), s, "", false)
			if err != nil {
				return err
			}
			return t.Show(cmd.Context(), args[0], qr, print)
		},
	}
	show.Flags().BoolVar(&qr, "qr", false, "show a QR code to scan in the WireGuard app of a phone")
	show.Flags().BoolVar(&print, "print", false, "print the configuration, private key included")
	cmd.AddCommand(show)
	cmd.AddCommand(&cobra.Command{
		Use:   "add <device>",
		Short: "Let a new device onto the private network",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !deviceRe.MatchString(args[0]) {
				return fmt.Errorf("%q is not a name for a device: lowercase letters, digits and hyphens, such as phone or work-laptop", args[0])
			}
			t, err := openTunnel(cmd.Context(), s, "", false)
			if err != nil {
				return err
			}
			return t.Add(cmd.Context(), args[0])
		},
	})
	var removeYes bool
	remove := &cobra.Command{
		Use:   "remove <device>",
		Short: "Take a device off the private network, such as a lost phone; its keys stop working at once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := openTunnel(cmd.Context(), s, "", removeYes)
			if err != nil {
				return err
			}
			return t.Remove(cmd.Context(), args[0])
		},
	}
	remove.Flags().BoolVar(&removeYes, "yes", false, "go ahead without asking; needed without a terminal")
	cmd.AddCommand(remove)
	var all, server, rotateYes bool
	rotate := &cobra.Command{
		Use:   "rotate [device]",
		Short: "Give a device new keys, or every device, or the server; the old ones stop working at once",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (len(args) == 1) == (all || server) {
				return &usageError{err: errors.New("name one device, or pass --all or --server"), help: fmt.Sprintf("Run '%s --help' for usage.", cmd.CommandPath())}
			}
			t, err := openTunnel(cmd.Context(), s, "", rotateYes)
			if err != nil {
				return err
			}
			return t.Rotate(cmd.Context(), firstArg(args), all, server)
		},
	}
	rotate.Flags().BoolVar(&all, "all", false, "every device")
	rotate.Flags().BoolVar(&rotateYes, "yes", false, "go ahead without asking; needed without a terminal")
	rotate.Flags().BoolVar(&server, "server", false, "the server: every device then needs its new configuration")
	cmd.AddCommand(rotate)
	return cmd
}

func openTunnel(ctx context.Context, s *cli, name string, yes bool) (*ops.Tunnel, error) {
	p, err := pickProject(s, name)
	if err != nil {
		return nil, err
	}
	m, dir, err := ops.ProjectStack(ctx, p, "")
	if err != nil {
		return nil, err
	}
	return ops.OpenTunnel(s.ui(yes), yes, p, m, dir)
}
