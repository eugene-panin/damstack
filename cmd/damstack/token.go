package main

import (
	"fmt"
	"maps"
	"os/exec"
	"runtime"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

func tokenCommand(s *streams) *cobra.Command {
	var show bool
	cmd := &cobra.Command{
		Use:   "token [name]",
		Short: "Copy a token of the project you are in, such as the one of the Nomad admin page",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			p, err := pickProject(s, "")
			if err != nil {
				return err
			}
			m, _ := cachedStack(p)
			if m == nil || len(m.Tokens) == 0 {
				return fmt.Errorf("the stack of %s gives no tokens", p.Meta.Name)
			}
			names := slices.Sorted(maps.Keys(m.Tokens))
			if len(args) == 0 {
				fmt.Fprintf(s.out, "Tokens of %s: %s. damstack token <name> copies one.\n", p.Meta.Name, strings.Join(names, ", "))
				return nil
			}
			secret, ok := m.Tokens[args[0]]
			if !ok {
				return fmt.Errorf("no token %s; there are %s", args[0], strings.Join(names, ", "))
			}
			password, err := p.Password()
			if err != nil {
				return err
			}
			secrets, err := p.Secrets(password)
			if err != nil {
				return err
			}
			value, ok := secrets[secret].(string)
			if !ok || value == "" {
				return fmt.Errorf("the project has no %s token yet; damstack deploy makes it", args[0])
			}
			if !show {
				if err := copyText(value); err == nil {
					fmt.Fprintf(s.out, "The %s token is in the clipboard; paste it where the page asks.\n", args[0])
					return nil
				}
			}
			fmt.Fprintln(s.out, value)
			return nil
		},
	}
	cmd.Flags().BoolVar(&show, "print", false, "print the token instead of copying it")
	return cmd
}

// copyText puts text in the clipboard of this machine.
func copyText(text string) error {
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "darwin":
		cmd = exec.Command("pbcopy")
	case hasCommand("wl-copy"):
		cmd = exec.Command("wl-copy")
	case hasCommand("xclip"):
		cmd = exec.Command("xclip", "-selection", "clipboard")
	default:
		return fmt.Errorf("no clipboard")
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func hasCommand(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
