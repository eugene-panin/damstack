// Package settings is stack.yaml as the settings screen shows it: sections of
// fields, each with the comment above it as its help, changed one value at a
// time. A change rewrites only that value's text, so the comments, the blank
// lines and the order of the file stay as they were.
package settings

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Kind is how a field is changed.
type Kind int

const (
	// Text is a string, changed by typing it.
	Text Kind = iota
	// Number is a whole number.
	Number
	// Toggle is true or false.
	Toggle
	// List is a list of strings or numbers, items added and removed.
	List
	// Heading names the fields under it, a block of the file.
	Heading
	// Fixed is shown, not changed here: a block of lists, text on several
	// lines, or a value changing which would break the platform.
	Fixed
)

// Field is a value of stack.yaml.
type Field struct {
	// Path is the keys to the value from the top of the file.
	Path  []string
	Label string
	// Help is the comment above the value, in one paragraph.
	Help  string
	Kind  Kind
	Value string
	Items []string
	// Depth is how deep under its section the field is.
	Depth int
	// Once says the value is chosen once: changing it breaks what runs.
	Once bool
}

// Section is a block of stack.yaml: a top-level key, or one app under apps,
// or the top-level values that are no block, as General.
type Section struct {
	Title  string
	Path   []string
	Help   string
	Fields []Field
}

// Doc is a stack.yaml being changed.
type Doc struct {
	src  []byte
	root *yaml.Node
}

// Parse reads a stack.yaml.
func Parse(src []byte) (*Doc, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("stack.yaml is not a mapping of settings")
	}
	return &Doc{src: src, root: doc.Content[0]}, nil
}

// Bytes is the file as it is now.
func (d *Doc) Bytes() []byte { return bytes.Clone(d.src) }

// Sections are the blocks of the file, in its order.
func (d *Doc) Sections() []Section {
	general := Section{Title: "General"}
	var out []Section
	for i := 0; i+1 < len(d.root.Content); i += 2 {
		k, v := d.root.Content[i], d.root.Content[i+1]
		switch {
		case k.Value == "apps" && v.Kind == yaml.MappingNode:
			for j := 0; j+1 < len(v.Content); j += 2 {
				ak, av := v.Content[j], v.Content[j+1]
				s := Section{Title: ak.Value, Path: []string{"apps", ak.Value}, Help: help(ak)}
				if s.Help == "" && j == 0 {
					s.Help = help(k)
				}
				s.Fields = fields(av, s.Path, 0)
				out = append(out, s)
			}
		case v.Kind == yaml.MappingNode:
			s := Section{Title: label(k.Value), Path: []string{k.Value}, Help: help(k)}
			s.Fields = fields(v, s.Path, 0)
			out = append(out, s)
		default:
			f := field(k, v, []string{k.Value}, 0)
			// The name is the datacenter of Consul and Nomad: damstack
			// rename changes it, not a new value here.
			if k.Value == "name" {
				f.Kind = Fixed
			}
			general.Fields = append(general.Fields, f)
		}
	}
	if len(general.Fields) > 0 {
		out = append([]Section{general}, out...)
	}
	return out
}

func fields(m *yaml.Node, path []string, depth int) []Field {
	var out []Field
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		p := append(append([]string{}, path...), k.Value)
		if v.Kind == yaml.MappingNode && depth < 2 {
			out = append(out, Field{Path: p, Label: label(k.Value), Help: help(k), Kind: Heading, Depth: depth})
			out = append(out, fields(v, p, depth+1)...)
			continue
		}
		out = append(out, field(k, v, p, depth))
	}
	return out
}

func field(k, v *yaml.Node, path []string, depth int) Field {
	f := Field{Path: path, Label: label(k.Value), Help: help(k), Depth: depth}
	f.Once = strings.Contains(f.Help, "Chosen once")
	switch {
	case v.Kind == yaml.ScalarNode && (v.Style&(yaml.LiteralStyle|yaml.FoldedStyle)) == 0:
		f.Value = v.Value
		switch v.Tag {
		case "!!bool":
			f.Kind = Toggle
		case "!!int":
			f.Kind = Number
		}
	case v.Kind == yaml.SequenceNode && scalars(v) && editableList(v):
		f.Kind = List
		for _, item := range v.Content {
			f.Items = append(f.Items, item.Value)
		}
		f.Value = strings.Join(f.Items, ", ")
	default:
		f.Kind = Fixed
		f.Value = summary(v)
	}
	if f.Once && f.Kind != Heading {
		f.Kind = Fixed
	}
	return f
}

func scalars(seq *yaml.Node) bool {
	for _, item := range seq.Content {
		if item.Kind != yaml.ScalarNode {
			return false
		}
	}
	return true
}

// editableList is a list on one line, [a, b], or one item per line.
func editableList(seq *yaml.Node) bool {
	if seq.Style&yaml.FlowStyle == 0 {
		return true
	}
	last := seq.Line
	for _, item := range seq.Content {
		last = item.Line
	}
	return last == seq.Line
}

func summary(v *yaml.Node) string {
	switch v.Kind {
	case yaml.MappingNode:
		return fmt.Sprintf("%d settings", len(v.Content)/2)
	case yaml.SequenceNode:
		return fmt.Sprintf("%d items", len(v.Content))
	}
	if i := strings.IndexByte(v.Value, '\n'); i >= 0 {
		return v.Value[:i] + " …"
	}
	return v.Value
}

func label(key string) string { return strings.ReplaceAll(key, "_", " ") }

// help is a key's head comment as one paragraph.
func help(k *yaml.Node) string {
	var parts []string
	for _, l := range strings.Split(k.HeadComment, "\n") {
		l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "#"))
		if l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, " ")
}

// find is the value at path.
func (d *Doc) find(path []string) (*yaml.Node, error) {
	n := d.root
	for _, key := range path {
		if n.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s: not a block of settings", strings.Join(path, "."))
		}
		var next *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				next = n.Content[i+1]
			}
		}
		if next == nil {
			return nil, fmt.Errorf("%s: no such setting", strings.Join(path, "."))
		}
		n = next
	}
	return n, nil
}

// Set changes the value at path, a text, number or true or false.
func (d *Doc) Set(path []string, value string) error {
	n, err := d.find(path)
	if err != nil {
		return err
	}
	if n.Kind != yaml.ScalarNode || n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return fmt.Errorf("%s is changed in the file, not here", strings.Join(path, "."))
	}
	token, err := scalarToken(n.Tag, value, n.Style, false)
	if err != nil {
		return fmt.Errorf("%s: %w", label(path[len(path)-1]), err)
	}
	line, start, end, err := d.scalarSpan(n)
	if err != nil {
		return err
	}
	return d.replaceInLine(line, start, end, token)
}

// SetList changes the list at path.
func (d *Doc) SetList(path []string, items []string) error {
	n, err := d.find(path)
	if err != nil {
		return err
	}
	if n.Kind != yaml.SequenceNode || !scalars(n) || !editableList(n) {
		return fmt.Errorf("%s is changed in the file, not here", strings.Join(path, "."))
	}
	tag, style := "!!str", yaml.Style(0)
	if len(n.Content) > 0 {
		tag, style = n.Content[0].Tag, n.Content[0].Style
	}
	flow := n.Style&yaml.FlowStyle != 0
	tokens := make([]string, len(items))
	for i, item := range items {
		if tokens[i], err = scalarToken(tag, item, style, flow); err != nil {
			return fmt.Errorf("%s: %w", label(path[len(path)-1]), err)
		}
	}
	if flow {
		line := n.Line - 1
		start := runeOffset(d.line(line), n.Column-1)
		end := flowEnd(d.line(line)[start:])
		if end < 0 {
			return fmt.Errorf("%s: the list does not end on its line", strings.Join(path, "."))
		}
		return d.replaceInLine(line, start, start+end+1, "["+strings.Join(tokens, ", ")+"]")
	}
	// One item per line: those lines give way to the new ones, indented as
	// the first was; none left makes it [] after its key.
	first, last := n.Content[0].Line-1, n.Content[len(n.Content)-1].Line-1
	firstLine := d.line(first)
	indent := string(firstLine[:len(firstLine)-len(bytes.TrimLeft(firstLine, " "))])
	lines := d.lines()
	var repl [][]byte
	for _, t := range tokens {
		repl = append(repl, []byte(indent+"- "+t))
	}
	out := append(append(append([][]byte{}, lines[:first]...), repl...), lines[last+1:]...)
	if len(tokens) == 0 {
		key := first - 1
		for key > 0 && len(bytes.TrimSpace(out[key])) == 0 {
			key--
		}
		out[key] = append(bytes.TrimRight(out[key], " "), []byte(" []")...)
	}
	return d.reparse(bytes.Join(out, []byte("\n")))
}

// flowEnd is the offset of the ] that closes the [ text starts with, past
// quoted items and nested lists; -1 when the line has none.
func flowEnd(text []byte) int {
	depth := 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '"':
			for i++; i < len(text) && text[i] != '"'; i++ {
				if text[i] == '\\' {
					i++
				}
			}
		case '\'':
			for i++; i < len(text) && !(text[i] == '\'' && (i+1 >= len(text) || text[i+1] != '\'')); i++ {
				if text[i] == '\'' {
					i++
				}
			}
		case '[':
			depth++
		case ']':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// scalarToken is how value is written in YAML for a value of tag: a number
// or true or false as it is, a text in the quotes the old one had, or quoted
// when it would read as something else; in a list on one line (flow) also
// when it holds what ends an item there.
func scalarToken(tag, value string, style yaml.Style, flow bool) (string, error) {
	switch tag {
	case "!!int":
		if _, err := strconv.Atoi(strings.TrimSpace(value)); err != nil {
			return "", fmt.Errorf("%q is not a whole number", value)
		}
		return strings.TrimSpace(value), nil
	case "!!bool":
		switch v := strings.TrimSpace(value); v {
		case "true", "false":
			return v, nil
		}
		return "", fmt.Errorf("%q is not true or false", value)
	}
	if strings.Contains(value, "\n") {
		return "", errors.New("one line only")
	}
	style &= yaml.DoubleQuotedStyle | yaml.SingleQuotedStyle
	if style == 0 && flow && strings.ContainsAny(value, ",[]{}#") {
		style = yaml.DoubleQuotedStyle
	}
	var v any = value
	if style != 0 {
		v = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: style}
	}
	out, err := yaml.Marshal(v)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// scalarSpan is where a one-line scalar's text is: its line and its byte
// offsets in it.
func (d *Doc) scalarSpan(n *yaml.Node) (int, int, int, error) {
	line := n.Line - 1
	text := d.line(line)
	start := runeOffset(text, n.Column-1)
	rest := text[start:]
	switch {
	case n.Style&yaml.DoubleQuotedStyle != 0:
		for i := 1; i < len(rest); i++ {
			switch rest[i] {
			case '\\':
				i++
			case '"':
				return line, start, start + i + 1, nil
			}
		}
	case n.Style&yaml.SingleQuotedStyle != 0:
		for i := 1; i < len(rest); i++ {
			if rest[i] == '\'' {
				if i+1 < len(rest) && rest[i+1] == '\'' {
					i++
					continue
				}
				return line, start, start + i + 1, nil
			}
		}
	default:
		// A plain value is written as it reads, even in {a: 1, b: 2}.
		if bytes.HasPrefix(rest, []byte(n.Value)) {
			return line, start, start + len(n.Value), nil
		}
		end := len(rest)
		if i := bytes.Index(rest, []byte(" #")); i >= 0 {
			end = i
		}
		end = len(bytes.TrimRight(rest[:end], " \t"))
		return line, start, start + end, nil
	}
	return 0, 0, 0, fmt.Errorf("line %d: a value on several lines is changed in the file, not here", n.Line)
}

func (d *Doc) lines() [][]byte { return bytes.Split(d.src, []byte("\n")) }

func (d *Doc) line(i int) []byte {
	lines := d.lines()
	if i < 0 || i >= len(lines) {
		return nil
	}
	return lines[i]
}

func (d *Doc) replaceInLine(i, start, end int, token string) error {
	lines := d.lines()
	l := lines[i]
	lines[i] = append(append(append([]byte{}, l[:start]...), token...), l[end:]...)
	return d.reparse(bytes.Join(lines, []byte("\n")))
}

func (d *Doc) reparse(src []byte) error {
	next, err := Parse(src)
	if err != nil {
		return fmt.Errorf("the change would break stack.yaml: %w", err)
	}
	*d = *next
	return nil
}

// runeOffset is the byte offset of the n-th rune of a line.
func runeOffset(line []byte, n int) int {
	off := 0
	for i := 0; i < n && off < len(line); i++ {
		_, size := utf8.DecodeRune(line[off:])
		off += size
	}
	return off
}
