package ansibleevents

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestFollowReadsThePlayWhileItIsWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	ctx, stop := context.WithCancel(context.Background())
	got := make(chan []Task)
	go func() {
		var tasks []Task
		if err := Follow(ctx, path, func(task Task) { tasks = append(tasks, task) }); err != nil {
			t.Error(err)
		}
		got <- tasks
	}()

	time.Sleep(2 * poll) // the play has not started: no file yet
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"type": "start", "name": "Run the Consul agent"}` + "\n")
	f.WriteString(`{"type": "changed", "name": "Run the Consul agent"}` + "\n")
	time.Sleep(2 * poll)
	f.WriteString(`{"type": "start", "name": "restart nomad", "handler": true}` + "\n")
	f.WriteString(`{"type": "skipped", "name": "restart nomad"`) // cut mid-line
	time.Sleep(2 * poll)
	f.WriteString("}\nnot json\n")
	f.Close()
	stop() // the play ended

	want := []Task{
		{Name: "Run the Consul agent", Status: Started},
		{Name: "Run the Consul agent", Status: Changed},
		{Name: "restart nomad", Status: Started, Handler: true},
		{Name: "restart nomad", Status: Skipped},
	}
	if tasks := <-got; !slices.Equal(tasks, want) {
		t.Errorf("got %+v\nwant %+v", tasks, want)
	}
}

func TestCountReadsListTasks(t *testing.T) {
	data, err := os.ReadFile("testdata/list-tasks.txt") // hashi's provision.yml
	if err != nil {
		t.Fatal(err)
	}
	if n := Count(string(data)); n != 40 {
		t.Errorf("got %d tasks, want 40 (the play line has TAGS too, and is no task)", n)
	}
}

func TestNamesAreTheTasksAPlayWentThroughInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prev.jsonl")
	os.WriteFile(path, []byte(`{"type":"start","name":"a"}
{"type":"ok","name":"a"}
{"type":"start","name":"b"}
{"type":"start","name":"a"}
`), 0o644)
	if got := Names(path); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("got %v", got)
	}
	if Names(filepath.Join(t.TempDir(), "none")) != nil {
		t.Error("no file must mean no names")
	}
}
