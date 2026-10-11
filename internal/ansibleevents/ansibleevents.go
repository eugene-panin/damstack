// Package ansibleevents follows a play as it runs: the Ansible callback plugin
// it carries (damstack_events) writes a JSON line for each task that starts
// and each result to a file, and Follow reads that file while the play goes.
package ansibleevents

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"
)

// Plugin is the callback plugin, damstack_events.py. Put it in a directory in
// ANSIBLE_CALLBACK_PLUGINS, enable it with ANSIBLE_CALLBACKS_ENABLED and name
// the file it writes to in DAMSTACK_EVENTS.
//
//go:embed damstack_events.py
var Plugin []byte

// PluginName is what ANSIBLE_CALLBACKS_ENABLED names it by.
const PluginName = "damstack_events"

// Status is where a task is.
type Status int

const (
	Started Status = iota
	OK
	Changed
	Failed
	Skipped
)

// Task is a task of the play that started or finished.
type Task struct {
	Name    string
	Status  Status
	Handler bool
}

var statuses = map[string]Status{
	"start": Started, "ok": OK, "changed": Changed, "failed": Failed, "skipped": Skipped,
}

// poll is how often Follow looks for more lines.
const poll = 200 * time.Millisecond

// Follow reads the file at path as the plugin writes it and calls report for
// each task, until ctx ends; it then reads what is left and returns. The file
// need not exist yet when Follow starts.
func Follow(ctx context.Context, path string, report func(Task)) error {
	var (
		f       *os.File
		r       *bufio.Reader
		partial []byte
	)
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	for {
		done := ctx.Err() != nil
		if f == nil {
			var err error
			if f, err = os.Open(path); err == nil {
				r = bufio.NewReader(f)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if r != nil {
			for {
				line, err := r.ReadBytes('\n')
				partial = append(partial, line...)
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
				parse(bytes.TrimSpace(partial), report)
				partial = partial[:0]
			}
		}
		if done {
			if len(partial) > 0 {
				parse(bytes.TrimSpace(partial), report)
			}
			return nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(poll):
		}
	}
}

func parse(line []byte, report func(Task)) {
	var m struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Handler bool   `json:"handler"`
	}
	if len(line) == 0 || json.Unmarshal(line, &m) != nil {
		return
	}
	if status, ok := statuses[m.Type]; ok {
		report(Task{Name: m.Name, Status: status, Handler: m.Handler})
	}
}

// Count is the number of tasks in the output of ansible-playbook
// --list-tasks: those it can see before the play runs, so roles included at
// run time are not in it.
func Count(listTasks string) int {
	n := 0
	inTasks := false
	for _, line := range strings.Split(listTasks, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "tasks:":
			inTasks = true
		case strings.HasPrefix(trimmed, "play #"):
			inTasks = false
		case inTasks && strings.Contains(line, "TAGS:"):
			n++
		}
	}
	return n
}

// Names are the tasks a play's events file shows starting, in order, each
// once: what the same play is expected to go through next time. None when the
// file cannot be read.
func Names(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var names []string
	seen := map[string]bool{}
	for _, line := range bytes.Split(data, []byte("\n")) {
		parse(bytes.TrimSpace(line), func(t Task) {
			if t.Status == Started && !seen[t.Name] {
				seen[t.Name] = true
				names = append(names, t.Name)
			}
		})
	}
	return names
}
