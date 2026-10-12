package project

import (
	"testing"
	"time"
)

func TestANewValueIsRecordedAsSetAndNoLongerSeen(t *testing.T) {
	p := &Project{Dir: t.TempDir(), Meta: Meta{Name: "ovh"}}
	if err := p.SaveSecrets("pw", map[string]any{"a": "1", "b": "2"}); err != nil {
		t.Fatal(err)
	}
	r, err := p.Records()
	if err != nil {
		t.Fatal(err)
	}
	first := r.Secrets["a"].Set
	if first.IsZero() || r.Secrets["b"].Set.IsZero() {
		t.Fatalf("new secrets must be recorded as set: %+v", r)
	}
	if err := p.MarkSeen("a", "printed by damstack token", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := p.MarkSet([]string{"account:sauron:ops"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := p.MarkSeen("b", "pasted in a chat", time.Now()); err != nil {
		t.Fatal(err)
	}

	// a is kept as it was, b is made anew, c is new: b loses its seen mark.
	if err := p.SaveSecrets("pw", map[string]any{"a": "1", "b": "3", "c": "4"}); err != nil {
		t.Fatal(err)
	}
	r, _ = p.Records()
	if r.Secrets["a"].Seen.IsZero() || !r.Secrets["a"].Set.Equal(first) {
		t.Errorf("a kept its value: still seen, set as before: %+v", r.Secrets["a"])
	}
	if !r.Secrets["b"].Seen.IsZero() {
		t.Errorf("b has a new value: no longer seen: %+v", r.Secrets["b"])
	}
	if r.Secrets["c"].Set.IsZero() {
		t.Error("c is new: recorded as set")
	}
	if _, ok := r.Secrets["account:sauron:ops"]; !ok {
		t.Error("a record of a secret outside vault.yml must survive saving vault.yml")
	}

	if err := p.SaveSecrets("pw", map[string]any{"a": "1"}); err != nil {
		t.Fatal(err)
	}
	if r, _ = p.Records(); len(r.Secrets) != 2 {
		t.Errorf("removed secrets lose their records, others stay: %+v", r.Secrets)
	}
}

func TestOldAfterHasADefault(t *testing.T) {
	if d := (Records{}).OldAfterDays(); d != DefaultOldAfter {
		t.Errorf("got %d", d)
	}
	if d := (Records{OldAfter: 30}).OldAfterDays(); d != 30 {
		t.Errorf("got %d", d)
	}
}
