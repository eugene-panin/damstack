package main

import (
	"encoding/json"
	"io"
	"time"

	"github.com/eugene-panin/damstack/internal/project"
)

// The --json output of status and history. The field names are an interface
// for scripts: add fields, never rename or drop one. They are their own types,
// so the history file can change without breaking it.

type statusJSON struct {
	Project string      `json:"project"`
	Dir     string      `json:"dir"`
	Stack   stackJSON   `json:"stack"`
	Apps    []stackJSON `json:"apps"`
	Created time.Time   `json:"created"`
	Steps   []stepJSON  `json:"steps"`
}

type stackJSON struct {
	Name   string `json:"name"`
	Tag    string `json:"tag"`
	Commit string `json:"commit"`
	URL    string `json:"url"`
}

// stepJSON is a step and its last deploy; last_run is null and result empty
// for a step that never ran.
type stepJSON struct {
	Step    string     `json:"step"`
	LastRun *time.Time `json:"last_run"`
	Result  string     `json:"result"`
	Stack   string     `json:"stack"`
}

type runJSON struct {
	Time     time.Time `json:"time"`
	Command  string    `json:"command"`
	Step     string    `json:"step"`
	Result   string    `json:"result"`
	Seconds  float64   `json:"seconds"`
	Stack    string    `json:"stack"`
	Commit   string    `json:"commit"`
	Damstack string    `json:"damstack"`
	Error    string    `json:"error"`
}

func newStackJSON(ref project.StackRef) stackJSON {
	return stackJSON{Name: ref.Name, Tag: ref.Tag, Commit: ref.Commit, URL: ref.URL}
}

func newStatusJSON(p *project.Project, steps []stepRun) statusJSON {
	st := statusJSON{
		Project: p.Meta.Name,
		Dir:     p.Dir,
		Stack:   newStackJSON(p.Meta.Stack),
		Apps:    []stackJSON{},
		Created: p.Meta.Created,
		Steps:   []stepJSON{},
	}
	for _, a := range p.Meta.Apps {
		st.Apps = append(st.Apps, newStackJSON(a))
	}
	for _, s := range steps {
		j := stepJSON{Step: s.name}
		if s.last != nil {
			t := s.last.Time
			j.LastRun, j.Result, j.Stack = &t, s.last.Result, s.last.Stack
		}
		st.Steps = append(st.Steps, j)
	}
	return st
}

func newRunJSON(e project.Entry) runJSON {
	return runJSON{Time: e.Time, Command: e.Command, Step: e.Step, Result: e.Result, Seconds: e.Seconds,
		Stack: e.Stack, Commit: e.Commit, Damstack: e.Damstack, Error: e.Error}
}

// writeJSON prints v as one indented JSON document.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
