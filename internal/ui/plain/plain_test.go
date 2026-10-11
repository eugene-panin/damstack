package plain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eugene-panin/damstack/internal/event"
)

// The lines a deploy printed before events existed, kept exactly.
func TestADeployReadsAsItAlwaysHas(t *testing.T) {
	var b strings.Builder
	r := New(&b)
	bootstrap := event.Step{N: 1, Total: 3, Name: "bootstrap", Title: "Securing the server"}
	provision := event.Step{N: 2, Total: 3, Name: "provision", Title: "Consul, Vault, Nomad and WireGuard"}
	apply := event.Step{N: 3, Total: 3, Name: "sauron/apply", Title: "sauron: Sauron"}
	for _, e := range []event.Event{
		event.Waiting{Holder: "aleksandr on air: deploy, since 12:04", Ahead: 1},
		event.Waiting{Ahead: 0},
		event.StepSkipped{Step: bootstrap, Reason: "done before, runs once"},
		event.StepStarted{Step: provision, Takes: "6 min"},
		event.StepDone{Step: provision, Took: 5*time.Minute + 2400*time.Millisecond},
		event.StepStarted{Step: apply},
		event.Note{Text: "Checking the plan against the policies of the stack.", Break: true},
		event.Note{Text: "Nothing to change."},
		event.StepFailed{Step: apply, Err: errors.New("tofu failed"), Log: "/p/.damstack/work/logs/x.log", Tail: []string{"Error: boom", "  on main.tf"}},
		event.Heading{Text: "policy"},
	} {
		r.Emit(e)
	}
	want := "Waiting: aleksandr on air: deploy, since 12:04. You are next.\n" +
		"\n1/3  Securing the server: done before, runs once\n" +
		"\n2/3  Consul, Vault, Nomad and WireGuard (6 min)\n" +
		"     ok, 5m2s\n" +
		"\n3/3  sauron: Sauron\n" +
		"\nChecking the plan against the policies of the stack.\n" +
		"Nothing to change.\n" +
		"  | Error: boom\n" +
		"  |   on main.tf\n" +
		"  The whole output is in /p/.damstack/work/logs/x.log\n" +
		"\n== policy\n"
	if got := b.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

func TestWaitingBehindSeveral(t *testing.T) {
	var b strings.Builder
	r := New(&b)
	r.Emit(event.Waiting{Holder: "eugene on mac: deploy, since 09:40", Ahead: 3})
	r.Emit(event.Waiting{Ahead: 2})
	want := "Waiting: eugene on mac: deploy, since 09:40. 3 ahead of you.\nWaiting for 2 ahead of you.\n"
	if got := b.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
