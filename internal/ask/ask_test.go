package ask

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"text/template"

	"github.com/eugene-panin/damstack/internal/checks"
	"github.com/eugene-panin/damstack/internal/manifest"
)

var questions = []manifest.Question{
	{Name: "address", Prompt: "Server address", Pattern: `^[0-9.]+$`},
	{Name: "user", Prompt: "Ops user", Default: "ops"},
	{Name: "provider", Prompt: "Provider", Type: "enum", Options: []string{"ssh", "ovh"}, Default: "ssh"},
	{Name: "clients", Prompt: "Devices", Type: "list", Default: []any{"laptop"}},
	{Name: "mail", Prompt: "Mail?", Type: "bool"},
	{Name: "mail_domains", Prompt: "Mail domains", Type: "list", When: "mail"},
}

func TestAsksUntilValid(t *testing.T) {
	in := strings.Join([]string{"not an ip", "10.0.0.1", "", "hetzner", "ovh", "laptop, phone,", "maybe", "y", "", "a.com,b.com"}, "\n") + "\n"
	var out bytes.Buffer
	got, err := Questions(NewPrompter(strings.NewReader(in), &out), questions, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"address": "10.0.0.1", "user": "ops", "provider": "ovh",
		"clients": []any{"laptop", "phone"}, "mail": true, "mail_domains": []any{"a.com", "b.com"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, text := range []string{`"not an ip" does not look right`, "must be one of ssh, ovh", "answer y or n", "give at least one",
		"Ops user [ops]: ", "Provider (ssh, ovh) [ssh]: ", "Devices, separated by commas [laptop]: ", "Mail? [y/N]: "} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("the output has no %q:\n%s", text, out.String())
		}
	}
}

func TestSkippedQuestionIsEmpty(t *testing.T) {
	got, err := Questions(NewPrompter(strings.NewReader("1.2.3.4\n\n\n\nn\n"), &bytes.Buffer{}), questions, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["mail_domains"], []any{}) || got["mail"] != false {
		t.Errorf("got %v", got)
	}
}

func TestInputEnds(t *testing.T) {
	if _, err := Questions(NewPrompter(strings.NewReader("1.2.3.4\n"), &bytes.Buffer{}), questions, nil, nil, nil); !errors.Is(err, ErrNoInput) {
		t.Errorf("got %v", err)
	}
}

func TestGivenAnswers(t *testing.T) {
	given := map[string]any{"address": "10.0.0.2", "clients": "a, b", "mail": true, "mail_domains": []any{"x.org"}}
	got, err := Questions(nil, questions, given, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["user"] != "ops" || got["provider"] != "ssh" || !reflect.DeepEqual(got["clients"], []any{"a", "b"}) {
		t.Errorf("got %v", got)
	}

	for _, tc := range []struct {
		given map[string]any
		want  string
	}{
		{map[string]any{"address": "x"}, "address: \"x\" does not look right"},
		{map[string]any{"address": "1.1.1.1", "mail": "yes"}, "mail: must be true or false"},
		{map[string]any{"address": "1.1.1.1", "clients": []any{1}}, "clients: item 1 must be text"},
		{map[string]any{"address": "1.1.1.1", "typo": "x"}, "typo is not a question"},
		{map[string]any{"address": "1.1.1.1", "mail": true}, "mail_domains: no answer given"},
		{map[string]any{}, "address: no answer given"},
	} {
		if _, err := Questions(nil, questions, tc.given, nil, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: got %v, want %q", tc.given, err, tc.want)
		}
	}
}

func TestSecretAndConfirm(t *testing.T) {
	p := NewPrompter(strings.NewReader("\n  token \nwhat\n\n"), &bytes.Buffer{})
	if s, err := p.Secret("Token"); err != nil || s != "token" {
		t.Errorf("secret %q, %v", s, err)
	}
	if ok, err := p.Confirm("Go on?", true); err != nil || !ok {
		t.Errorf("confirm %v, %v", ok, err)
	}
}

func TestTemplateDefaultAndAdvanced(t *testing.T) {
	qs := []manifest.Question{
		{Name: "domains", Prompt: "Domains", Type: "list"},
		{Name: "hostname", Prompt: "Host", Default: "mail.{{ index .domains 0 }}"},
		{Name: "cidr", Prompt: "Network", Default: "{{ freeSubnet }}", Advanced: true},
	}
	funcs := template.FuncMap{"freeSubnet": func() string { return "10.64.3.0/24" }}
	var out bytes.Buffer
	got, err := Questions(NewPrompter(strings.NewReader("example.org, example.net\n\n"), &out), qs, nil, funcs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["hostname"] != "mail.example.org" || got["cidr"] != "10.64.3.0/24" {
		t.Errorf("got %v", got)
	}
	if !strings.Contains(out.String(), "Host [mail.example.org]: ") || strings.Contains(out.String(), "Network") {
		t.Errorf("asked:\n%s", out.String())
	}
	given, err := Questions(nil, qs, map[string]any{"domains": []any{"a.org"}, "cidr": "10.99.0.0/24"}, funcs, nil)
	if err != nil || given["cidr"] != "10.99.0.0/24" || given["hostname"] != "mail.a.org" {
		t.Errorf("given %v, %v", given, err)
	}
}

func TestChecksAskAgain(t *testing.T) {
	qs := []manifest.Question{{Name: "address", Prompt: "Address", Checks: []manifest.CheckSpec{{Name: "ssh-port"}}}}
	check := func(_ []manifest.CheckSpec, v any, _ map[string]any) []checks.Result {
		if v == "10.0.0.1" {
			return []checks.Result{{OK: true, Text: "it answers on port 22"}}
		}
		return []checks.Result{{OK: false, Text: "nothing answers on port 22"}}
	}
	var out bytes.Buffer
	got, err := Questions(NewPrompter(strings.NewReader("10.0.0.9\n10.0.0.1\n"), &out), qs, nil, nil, check)
	if err != nil || got["address"] != "10.0.0.1" {
		t.Fatalf("%v, %v", got, err)
	}
	if !strings.Contains(out.String(), "FAIL  nothing answers") || strings.Count(out.String(), "Address: ") != 2 {
		t.Errorf("output:\n%s", out.String())
	}
	if _, err := Questions(nil, qs, map[string]any{"address": "10.0.0.9"}, nil, check); err == nil || !strings.Contains(err.Error(), "address: nothing answers") {
		t.Errorf("a given answer that fails: %v", err)
	}
}

// Without a terminal nothing is asked or printed: answers come from the file.
func TestNoTerminal(t *testing.T) {
	var out bytes.Buffer
	p := NewPrompter(strings.NewReader("y\n"), &out)
	p.NoTerminal = true
	if _, err := p.Line("Name: "); !errors.Is(err, ErrNoTerminal) {
		t.Errorf("Line: %v", err)
	}
	if _, err := p.Confirm("Go on?", true); !errors.Is(err, ErrNoTerminal) {
		t.Errorf("Confirm: %v", err)
	}
	if out.Len() > 0 {
		t.Errorf("printed %q", out.String())
	}

	answers, err := Questions(p, questions, map[string]any{"address": "10.0.0.1"}, nil, nil)
	if err != nil || answers["address"] != "10.0.0.1" || answers["user"] != "ops" {
		t.Errorf("given and defaults: %v, %v", answers, err)
	}
	if _, err := Questions(p, questions, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "--answers") {
		t.Errorf("missing answer: %v", err)
	}
	if out.Len() > 0 {
		t.Errorf("questions printed %q", out.String())
	}
}

// A secret can still be piped in, read without a prompt.
func TestNoTerminalSecretFromPipe(t *testing.T) {
	var out bytes.Buffer
	p := NewPrompter(strings.NewReader("s3cret\n"), &out)
	p.NoTerminal = true
	if got, err := p.Secret("Passphrase"); err != nil || got != "s3cret" || out.Len() > 0 {
		t.Errorf("got %q, %v, printed %q", got, err, out.String())
	}
}
