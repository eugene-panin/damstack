package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/project"
)

// projectArg is the project named with -p or --project anywhere in the
// arguments; run takes it out before cobra parses them, so that it reaches
// the commands of stacks too.
var projectArg string

func takeProjectFlag(args []string) []string {
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "-p" || a == "--project") && i+1 < len(args):
			projectArg = args[i+1]
			i++
		case strings.HasPrefix(a, "--project="):
			projectArg = strings.TrimPrefix(a, "--project=")
		default:
			rest = append(rest, a)
		}
	}
	return rest
}

// pickProject is the project a command works on: the one named, else the
// one the working directory is in, else the current one, else the only one,
// else the one picked from a list.
func pickProject(s *streams, name string) (*project.Project, error) {
	p, cfg, err := resolveProject(name)
	if p != nil || err != nil {
		return p, err
	}
	if len(cfg.Projects) == 0 {
		return nil, errNoProject
	}
	if s == nil || !s.tty() {
		return nil, fmt.Errorf("there are %d projects: name one, such as damstack status %s, or make one current with damstack use",
			len(cfg.Projects), cfg.Projects[0].Name)
	}
	fmt.Fprintln(s.err, "Which project?")
	for i, entry := range cfg.Projects {
		fmt.Fprintf(s.err, "  %d. %s\n", i+1, entry.Name)
	}
	for {
		answer, err := s.prompt.Line("Which? ")
		if err != nil {
			return nil, err
		}
		if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(cfg.Projects) {
			answer = cfg.Projects[n-1].Name
		}
		if entry, ok := cfg.Project(answer); ok {
			fmt.Fprintf(s.out, "damstack use %s makes it current, so that it is not asked again.\n", entry.Name)
			return project.Open(entry.Path)
		}
		fmt.Fprintf(s.out, "  answer a number from 1 to %d\n", len(cfg.Projects))
	}
}

// resolveProject finds the project without asking; nil and no error when it
// takes a choice.
func resolveProject(name string) (*project.Project, *config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	if name == "" {
		name = projectArg
	}
	if name != "" {
		entry, ok := cfg.Project(name)
		if !ok {
			return nil, cfg, fmt.Errorf("no project named %s; damstack lists them", name)
		}
		p, err := project.Open(entry.Path)
		return p, cfg, err
	}
	if wd, err := os.Getwd(); err == nil {
		if p, err := project.Find(wd); err != nil || p != nil {
			return p, cfg, err
		}
	}
	if entry, ok := cfg.Project(cfg.Current); ok {
		p, err := project.Open(entry.Path)
		return p, cfg, err
	}
	if len(cfg.Projects) == 1 {
		p, err := project.Open(cfg.Projects[0].Path)
		return p, cfg, err
	}
	return nil, cfg, nil
}

func useCommand(s *streams) *cobra.Command {
	return &cobra.Command{
		Use:   "use <project>",
		Short: "Make a project current: commands work on it unless another is named",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if _, ok := cfg.Project(args[0]); !ok {
				return fmt.Errorf("no project named %s; damstack lists them", args[0])
			}
			cfg.Current = args[0]
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(s.out, "%s is the current project.\n", args[0])
			return nil
		},
	}
}

func editCommand(s *streams) *cobra.Command {
	return &cobra.Command{
		Use:   "edit [project]",
		Short: "Open the stack.yaml of a project in your editor, check it, and offer to deploy it",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			p, err := pickProject(s, name)
			if err != nil {
				return err
			}
			path := filepath.Join(p.Dir, project.ConfigFile)
			before, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for {
				if err := openEditor(s, path); err != nil {
					return err
				}
				after, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if string(after) == string(before) {
					fmt.Fprintln(s.out, "stack.yaml did not change.")
					return nil
				}
				var check map[string]any
				if err := yaml.Unmarshal(after, &check); err == nil {
					break
				} else {
					fmt.Fprintf(s.out, "stack.yaml is not valid YAML: %v\n", err)
				}
				again, err := s.prompt.Confirm("Edit it again?", true)
				if err != nil {
					return err
				}
				if !again {
					return errors.New("stack.yaml is not valid YAML; damstack deploy refuses it until it is")
				}
			}
			ok, err := s.prompt.Confirm(fmt.Sprintf("Deploy %s now?", p.Meta.Name), true)
			if err != nil || !ok {
				return err
			}
			m, dir, err := projectStack(cmd.Context(), p, "")
			if err != nil {
				return err
			}
			return runSteps(cmd.Context(), s, p, m, dir, "", false)
		},
	}
}

func openEditor(s *streams, path string) error {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	fields := strings.Fields(editor)
	cmd := exec.Command(fields[0], append(fields[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, s.out, s.err
	return cmd.Run()
}
