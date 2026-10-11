package tofujson

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// A real apply's -json output (OpenTofu 1.12), cut short.
const apply = `{"@level":"info","@message":"OpenTofu 1.12.6","@module":"tofu.ui","type":"version","tofu":"1.12.6","ui":"1.2"}
{"@level":"info","@message":"nomad_job.web: Modifying... [id=web]","@module":"tofu.ui","hook":{"resource":{"addr":"nomad_job.web","module":"","resource":"nomad_job.web","implied_provider":"nomad","resource_type":"nomad_job","resource_name":"web","resource_key":null},"action":"update","id_key":"id","id_value":"web"},"type":"apply_start"}
{"@level":"info","@message":"consul_config_entry.defaults[0]: Creating...","@module":"tofu.ui","hook":{"resource":{"addr":"consul_config_entry.defaults[0]"},"action":"create"},"type":"apply_start"}
{"@level":"info","@message":"consul_config_entry.defaults[0]: Creation complete after 1s [id=service-defaults-web]","@module":"tofu.ui","hook":{"resource":{"addr":"consul_config_entry.defaults[0]"},"action":"create","id_key":"id","id_value":"service-defaults-web","elapsed_seconds":1},"type":"apply_complete"}
{"@level":"info","@message":"nomad_job.web: Still modifying... [id=web, 10s elapsed]","@module":"tofu.ui","hook":{"resource":{"addr":"nomad_job.web"},"action":"update","elapsed_seconds":10},"type":"apply_progress"}
{"@level":"error","@message":"nomad_job.web: Modification errored after 12s","@module":"tofu.ui","hook":{"resource":{"addr":"nomad_job.web"},"action":"update","elapsed_seconds":12},"type":"apply_errored"}
{"@level":"error","@message":"Error: job validation failed","@module":"tofu.ui","diagnostic":{"severity":"error","summary":"job validation failed","detail":""},"type":"diagnostic"}
{"@level":"info","@message":"Apply complete! Resources: 1 added, 0 changed, 0 destroyed.","@module":"tofu.ui","changes":{"add":1,"change":0,"import":0,"remove":0,"operation":"apply"},"type":"change_summary"}
`

func TestAnApplyIsTextForTheLogAndResourcesForTheProgress(t *testing.T) {
	var text strings.Builder
	var got []Resource
	w := NewWriter(&text, func(r Resource) { got = append(got, r) })
	// OpenTofu writes in pieces that cut lines anywhere.
	for chunk := range slices.Chunk([]byte(apply), 37) {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	want := []Resource{
		{Addr: "nomad_job.web", Type: "nomad_job", ID: "web", Action: "update", Status: Started},
		{Addr: "consul_config_entry.defaults[0]", Action: "create", Status: Started},
		{Addr: "consul_config_entry.defaults[0]", ID: "service-defaults-web", Action: "create", Status: Done, Elapsed: time.Second},
		{Addr: "nomad_job.web", Action: "update", Status: Failed, Elapsed: 12 * time.Second},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if !strings.Contains(text.String(), "consul_config_entry.defaults[0]: Creation complete after 1s [id=service-defaults-web]\n") ||
		!strings.Contains(text.String(), "Apply complete! Resources: 1 added, 0 changed, 0 destroyed.\n") ||
		strings.Contains(text.String(), `"@level"`) {
		t.Errorf("the log must read as OpenTofu's text, got:\n%s", text.String())
	}
}

func TestLinesThatAreNotJSONPassAsTheyAre(t *testing.T) {
	var text strings.Builder
	w := NewWriter(&text, nil)
	w.Write([]byte("panic: something\n{\"@message\":\"ok\",\"type\":\"log\"}\nno newline at the end"))
	w.Close()
	if want := "panic: something\nok\nno newline at the end\n"; text.String() != want {
		t.Errorf("got %q, want %q", text.String(), want)
	}
}
