package manifest

import (
	"bytes"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// TemplateFuncs are the functions the config template, and the server and
// env templates, may call besides the builtins of text/template. secret is
// bound to the project's secrets when a template runs.
var TemplateFuncs = template.FuncMap{
	"firstHost": func(cidr string) (string, error) {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			return "", fmt.Errorf("firstHost %q: %w", cidr, err)
		}
		return prefix.Masked().Addr().Next().String(), nil
	},
	"secret": func(string) (string, error) { return "", nil },
	"join": func(sep string, items []any) string {
		texts := make([]string, len(items))
		for i, item := range items {
			texts[i] = fmt.Sprint(item)
		}
		return strings.Join(texts, sep)
	},
	"yaml": toYAML,
	// freeSubnet is a /24 of 10.64.0.0/10 no other project and no network of
	// this machine uses; damstack binds it when a default asks for it.
	"freeSubnet": func() string { return "10.64.0.0/24" },
}

// RenderDefault runs the default of a question that is a template, on the
// answers before it, with funcs over TemplateFuncs.
func RenderDefault(name, text string, answers map[string]any, funcs template.FuncMap) (string, error) {
	t, err := template.New(name).Option("missingkey=error").Funcs(TemplateFuncs).Funcs(funcs).Parse(text)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := t.Execute(&out, answers); err != nil {
		return "", err
	}
	return out.String(), nil
}

// toYAML writes a value on one line, quoted where YAML needs it, so that an
// answer cannot break the stack.yaml it goes into.
func toYAML(v any) (string, error) {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return "", err
	}
	flow(&n)
	out, err := yaml.Marshal(&n)
	return strings.TrimSpace(string(out)), err
}

func flow(n *yaml.Node) {
	if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
		n.Style |= yaml.FlowStyle
	}
	for _, c := range n.Content {
		flow(c)
	}
}

// RenderConfig renders the config template of the stack in dir with the
// answers, and the project name as .project, into the stack.yaml of a new
// project.
func (m *Manifest) RenderConfig(dir, project string, answers map[string]any) ([]byte, error) {
	text, err := os.ReadFile(filepath.Join(dir, m.Config))
	if err != nil {
		return nil, err
	}
	data := map[string]any{"project": project}
	for k, v := range answers {
		data[k] = v
	}
	t, err := template.New(m.Config).Option("missingkey=error").Funcs(TemplateFuncs).Parse(string(text))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := t.Execute(&out, data); err != nil {
		return nil, err
	}
	var check map[string]any
	if err := yaml.Unmarshal(out.Bytes(), &check); err != nil {
		return nil, fmt.Errorf("%s gave a stack.yaml that is not YAML: %w", m.Config, err)
	}
	return out.Bytes(), nil
}

// Render runs a server or env template: .config is the project's stack.yaml,
// and secret "name" gives a secret, empty when the project has none of that
// name yet.
func Render(name, text string, config, secrets map[string]any) (string, error) {
	return RenderData(name, text, map[string]any{"config": config}, secrets)
}

// RenderData runs a template on data, such as .config and, for an app, .app,
// its own settings under apps of stack.yaml.
func RenderData(name, text string, data, secrets map[string]any) (string, error) {
	funcs := template.FuncMap{"secret": func(key string) (string, error) {
		switch v := secrets[key].(type) {
		case nil:
			return "", nil
		case string:
			return v, nil
		default:
			return toYAML(v)
		}
	}}
	t, err := template.New(name).Option("missingkey=error").Funcs(TemplateFuncs).Funcs(funcs).Parse(text)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := t.Execute(&out, data); err != nil {
		return "", err
	}
	return out.String(), nil
}
