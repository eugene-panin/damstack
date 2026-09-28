package ask

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

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
	got, err := Questions(NewPrompter(strings.NewReader(in), &out), questions, nil)
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
	got, err := Questions(NewPrompter(strings.NewReader("1.2.3.4\n\n\n\nn\n"), &bytes.Buffer{}), questions, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["mail_domains"], []any{}) || got["mail"] != false {
		t.Errorf("got %v", got)
	}
}

func TestInputEnds(t *testing.T) {
	if _, err := Questions(NewPrompter(strings.NewReader("1.2.3.4\n"), &bytes.Buffer{}), questions, nil); !errors.Is(err, ErrNoInput) {
		t.Errorf("got %v", err)
	}
}

func TestGivenAnswers(t *testing.T) {
	given := map[string]any{"address": "10.0.0.2", "clients": "a, b", "mail": true, "mail_domains": []any{"x.org"}}
	got, err := Questions(nil, questions, given)
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
		if _, err := Questions(nil, questions, tc.given); err == nil || !strings.Contains(err.Error(), tc.want) {
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
