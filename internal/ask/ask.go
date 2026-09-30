// Package ask asks the questions of a stack and checks the answers, typed in
// or read from a file.
package ask

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"text/template"

	"github.com/eugene-panin/damstack/internal/manifest"
)

// ErrNoInput is returned when the input ends before a question is answered.
var ErrNoInput = errors.New("the input ended before every question was answered")

type Prompter struct {
	In  *bufio.Reader
	Out io.Writer
	// Hidden reads a line without echoing it, for secrets; nil reads from In.
	Hidden func() (string, error)
}

func NewPrompter(in io.Reader, out io.Writer) *Prompter {
	return &Prompter{In: bufio.NewReader(in), Out: out}
}

func (p *Prompter) Line(prompt string) (string, error) {
	fmt.Fprint(p.Out, prompt)
	s, err := p.In.ReadString('\n')
	if errors.Is(err, io.EOF) && s == "" {
		fmt.Fprintln(p.Out)
		return "", ErrNoInput
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

// Confirm asks a yes or no question; an empty answer is def.
func (p *Prompter) Confirm(question string, def bool) (bool, error) {
	hint := " [y/N] "
	if def {
		hint = " [Y/n] "
	}
	for {
		raw, err := p.Line(question + hint)
		if err != nil {
			return false, err
		}
		if raw == "" {
			return def, nil
		}
		if b, ok := parseBool(raw); ok {
			return b, nil
		}
		fmt.Fprintln(p.Out, "  answer y or n")
	}
}

// Secret asks for a value that is not shown as it is typed, until one is given.
func (p *Prompter) Secret(prompt string) (string, error) {
	for {
		fmt.Fprint(p.Out, prompt+": ")
		var s string
		var err error
		if p.Hidden != nil {
			s, err = p.Hidden()
			fmt.Fprintln(p.Out)
		} else {
			s, err = p.In.ReadString('\n')
			if errors.Is(err, io.EOF) && s != "" {
				err = nil
			}
		}
		if errors.Is(err, io.EOF) {
			return "", ErrNoInput
		}
		if err != nil {
			return "", err
		}
		if s = strings.TrimSpace(s); s != "" {
			return s, nil
		}
		fmt.Fprintln(p.Out, "  an answer is required")
	}
}

// Questions asks every question that applies, in order. An answer in given,
// such as from a file, is checked instead of asked; with a nil prompter, and
// for an advanced question, the default is taken. A default that is a
// template sees the answers before it and funcs.
func Questions(p *Prompter, qs []manifest.Question, given map[string]any, funcs template.FuncMap) (map[string]any, error) {
	for name := range given {
		if !slices.ContainsFunc(qs, func(q manifest.Question) bool { return q.Name == name }) {
			return nil, fmt.Errorf("%s is not a question of the stack", name)
		}
	}
	answers := map[string]any{}
	section := ""
	for _, q := range qs {
		if !manifest.Holds(q.When, answers) {
			answers[q.Name] = zero(q)
			continue
		}
		if text, ok := q.Default.(string); ok && strings.Contains(text, "{{") {
			d, err := manifest.RenderDefault(q.Name, text, answers, funcs)
			if err != nil {
				return nil, fmt.Errorf("the default of %s: %w", q.Name, err)
			}
			q.Default = d
		}
		if v, ok := given[q.Name]; ok {
			a, err := Check(q, v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", q.Name, err)
			}
			answers[q.Name] = a
			continue
		}
		if p == nil || q.Advanced {
			if q.Default == nil && q.Type == "bool" {
				answers[q.Name] = false
				continue
			}
			if q.Default == nil {
				return nil, fmt.Errorf("%s: no answer given", q.Name)
			}
			a, err := Check(q, q.Default)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", q.Name, err)
			}
			answers[q.Name] = a
			continue
		}
		if q.Section != "" && q.Section != section {
			fmt.Fprintf(p.Out, "\n── %s\n", q.Section)
			section = q.Section
		}
		if q.Note != "" {
			fmt.Fprintln(p.Out, q.Note)
		}
		for {
			raw, err := p.Line(Prompt(q))
			if err != nil {
				return nil, err
			}
			a, err := Parse(q, raw)
			if err == nil {
				answers[q.Name] = a
				break
			}
			fmt.Fprintf(p.Out, "  %v\n", err)
		}
	}
	return answers, nil
}

// Prompt is the line a question is asked with.
func Prompt(q manifest.Question) string {
	var b strings.Builder
	b.WriteString(q.Prompt)
	switch q.Type {
	case "bool":
		if q.Default == true {
			b.WriteString(" [Y/n]")
		} else {
			b.WriteString(" [y/N]")
		}
		return b.String() + ": "
	case "list":
		b.WriteString(", separated by commas")
	case "enum":
		fmt.Fprintf(&b, " (%s)", strings.Join(q.Options, ", "))
	}
	switch d := q.Default.(type) {
	case string:
		fmt.Fprintf(&b, " [%s]", d)
	case []any:
		items := make([]string, len(d))
		for i, item := range d {
			items[i] = fmt.Sprint(item)
		}
		fmt.Fprintf(&b, " [%s]", strings.Join(items, ", "))
	}
	return b.String() + ": "
}

// Parse checks a typed answer; an empty one takes the default.
func Parse(q manifest.Question, raw string) (any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" && q.Default != nil {
		return Check(q, q.Default)
	}
	switch q.Type {
	case "bool":
		if raw == "" {
			return false, nil
		}
		b, ok := parseBool(raw)
		if !ok {
			return nil, errors.New("answer y or n")
		}
		return b, nil
	case "list":
		return checkList(q, strings.Split(raw, ","))
	default:
		return checkString(q, raw)
	}
}

// Check checks an answer given as a value, such as from a YAML file.
func Check(q manifest.Question, v any) (any, error) {
	switch q.Type {
	case "bool":
		b, ok := v.(bool)
		if !ok {
			return nil, errors.New("must be true or false")
		}
		return b, nil
	case "list":
		switch items := v.(type) {
		case string:
			return checkList(q, strings.Split(items, ","))
		case []string:
			return checkList(q, items)
		case []any:
			texts := make([]string, len(items))
			for i, item := range items {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("item %d must be text", i+1)
				}
				texts[i] = s
			}
			return checkList(q, texts)
		}
		return nil, errors.New("must be a list")
	default:
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("must be text")
		}
		return checkString(q, s)
	}
}

func checkString(q manifest.Question, s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", errors.New("an answer is required")
	case q.Type == "enum" && !slices.Contains(q.Options, s):
		return "", fmt.Errorf("must be one of %s", strings.Join(q.Options, ", "))
	case q.Pattern != "" && !regexp.MustCompile(q.Pattern).MatchString(s):
		return "", fmt.Errorf("%q does not look right: it must match %s", s, q.Pattern)
	}
	return s, nil
}

func checkList(q manifest.Question, raw []string) ([]any, error) {
	items := []any{}
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if q.Pattern != "" && !regexp.MustCompile(q.Pattern).MatchString(item) {
			return nil, fmt.Errorf("%q does not look right: it must match %s", item, q.Pattern)
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil, errors.New("give at least one, separated by commas")
	}
	return items, nil
}

func parseBool(s string) (bool, bool) {
	switch strings.ToLower(s) {
	case "y", "yes", "true":
		return true, true
	case "n", "no", "false":
		return false, true
	}
	return false, false
}

func zero(q manifest.Question) any {
	switch q.Type {
	case "bool":
		return false
	case "list":
		return []any{}
	}
	return ""
}
