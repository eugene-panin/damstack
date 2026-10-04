package main

import (
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/manifest"
)

// usageTemplate is cobra's, with the examples first.
const usageTemplate = `{{if .HasExample}}Examples:
{{.Example}}

{{end}}Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasAvailableSubCommands}}

Available Commands:{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`

// examples are shown first in the help of each command, by its path.
var examples = map[string]string{
	"deploy":          "  damstack deploy                     deploy the project you are in again\n  damstack deploy hashi               set up a new project on the hashi platform\n  damstack deploy hashi --name my-cloud --answers answers.yaml --yes\n                                      the same in a script, asking nothing",
	"add":             "  damstack add owner/name             github.com/owner/damstack-name",
	"app add":         "  damstack app add mail",
	"status":          "  damstack status\n  damstack status my-cloud --live     check the server now, changing nothing\n  damstack status --json | jq -r '.steps[] | select(.result != \"ok\") | .step'",
	"history":         "  damstack history my-cloud\n  damstack history --json | jq '.[-1]'   the last run",
	"use":             "  damstack use my-cloud",
	"edit":            "  damstack edit",
	"token":           "  damstack token                      list the tokens of the project\n  damstack token nomad --print",
	"stack lint":      "  damstack stack lint ~/src/damstack-mystack",
	"stack check":     "  damstack stack check",
	"backup pull":     "  damstack backup pull my-cloud",
	"backup schedule": "  damstack backup schedule my-cloud",
	"backup kit":      "  damstack backup kit my-cloud",
	"backup open":     "  damstack backup open ~/Downloads/my-cloud-kit-2026-10-04.age",
}

// projectCommands take a project name as their first argument.
var projectCommands = []string{"use", "edit", "status", "history", "backup status", "backup pull", "backup schedule", "backup kit"}

// describe adds the examples and the completions to the commands under root.
func describe(root *cobra.Command, s *streams) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		path := strings.TrimPrefix(c.CommandPath(), root.Name()+" ")
		if ex, ok := examples[path]; ok && c.Example == "" {
			c.Example = ex
		}
		if c.ValidArgsFunction == nil {
			switch {
			case slices.Contains(projectCommands, path):
				c.ValidArgsFunction = completeFirst(func(*cobra.Command) []string { return projectNames() })
			case path == "deploy":
				c.ValidArgsFunction = completeFirst(func(c *cobra.Command) []string {
					return append(projectNames(), stackNames(c, false)...)
				})
			case path == "app add":
				c.ValidArgsFunction = completeFirst(func(c *cobra.Command) []string { return stackNames(c, true) })
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	_ = root.RegisterFlagCompletionFunc("project", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return projectNames(), cobra.ShellCompDirectiveNoFileComp
	})
}

// completeFirst completes the first argument with names.
func completeFirst(names func(*cobra.Command) []string) cobra.CompletionFunc {
	return func(c *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return names(c), cobra.ShellCompDirectiveNoFileComp
	}
}

func projectNames() []string {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	var names []string
	for _, p := range cfg.Projects {
		names = append(names, p.Name)
	}
	return names
}

// stackNames are the platforms, or with apps the apps, damstack can deploy.
func stackNames(c *cobra.Command, apps bool) []string {
	cfg, err := loadConfig(c.Context())
	if err != nil {
		return nil
	}
	var names []string
	for _, st := range cfg.AllStacks() {
		if (st.Kind == manifest.KindApp) == apps {
			names = append(names, st.Name)
		}
	}
	return names
}
