package main

import (
	"fmt"
	"maps"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
)

func tokenCommand(s *streams) *cobra.Command {
	var show bool
	cmd := &cobra.Command{
		Use:   "token [name]",
		Short: "Copy a token of the project you are in, such as the one of the Nomad admin page",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := pickProject(s, "")
			if err != nil {
				return err
			}
			m, _, err := projectStack(c.Context(), p, "")
			if err != nil {
				return err
			}
			if len(m.Tokens) == 0 {
				return fmt.Errorf("the stack of %s gives no tokens", p.Meta.Name)
			}
			names := slices.Sorted(maps.Keys(m.Tokens))
			if len(args) == 0 {
				fmt.Fprintf(s.out, "Tokens of %s: %s. damstack token <name> copies one.\n", p.Meta.Name, strings.Join(names, ", "))
				return nil
			}
			if _, ok := m.Tokens[args[0]]; !ok {
				return fmt.Errorf("no token %s; there are %s", args[0], strings.Join(names, ", "))
			}
			value, err := projectToken(p, m, args[0])
			if err != nil {
				return err
			}
			if !show {
				if err := copyText(value); err != nil {
					return fmt.Errorf("the clipboard: %w; damstack token %s --print prints it instead", err, args[0])
				}
				fmt.Fprintf(s.out, "The %s token is in the clipboard; paste it where the page asks.\n", args[0])
				return nil
			}
			fmt.Fprintln(s.out, value)
			if s.tty() {
				// On a terminal it is in plain text now, and its scrollback.
				return p.MarkSeen(m.Tokens[args[0]], "printed by damstack token", time.Now())
			}
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

// projectToken is the value of the token name of a project's stack, from its
// secrets.
func projectToken(p *project.Project, m *manifest.Manifest, name string) (string, error) {
	secret, ok := m.Tokens[name]
	if !ok {
		return "", fmt.Errorf("the stack of %s gives no token %s", p.Meta.Name, name)
	}
	password, err := p.Password()
	if err != nil {
		return "", err
	}
	secrets, err := p.Secrets(password)
	if err != nil {
		return "", err
	}
	value, ok := secrets[secret].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("the project has no %s token yet; damstack deploy makes it", name)
	}
	return value, nil
}
