// Package tofujson reads the machine-readable output of OpenTofu (-json): one
// JSON object a line, each with the text OpenTofu would have printed in
// @message. It writes that text on, so a log reads as usual, and reports each
// resource as it starts, finishes or fails.
package tofujson

import (
	"bytes"
	"encoding/json"
	"io"
	"time"
)

// Status is where a resource is in an apply.
type Status int

const (
	Started Status = iota
	Done
	Failed
)

// Resource is a resource of an apply that changed status.
type Resource struct {
	Addr string
	// Type is the resource type, such as nomad_job; ID its id once applied.
	Type string
	ID   string
	// Action is what OpenTofu does to it: create, update, delete, replace, read.
	Action  string
	Status  Status
	Elapsed time.Duration
}

// Writer takes OpenTofu's -json output. It writes each message's text to W
// and calls Report for each resource that starts, finishes or fails. Lines
// that are not JSON go to W as they are. Close writes out a last unfinished
// line.
type Writer struct {
	W      io.Writer
	Report func(Resource)

	buf []byte
}

// NewWriter is a Writer passing text to w and resources to report.
func NewWriter(w io.Writer, report func(Resource)) *Writer {
	return &Writer{W: w, Report: report}
}

func (w *Writer) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		if err := w.line(w.buf[:i]); err != nil {
			return len(p), err
		}
		w.buf = w.buf[i+1:]
	}
}

// Close handles a last line that had no newline.
func (w *Writer) Close() error {
	if len(w.buf) == 0 {
		return nil
	}
	err := w.line(w.buf)
	w.buf = nil
	return err
}

// message is the part of a line of OpenTofu's -json output this package reads.
type message struct {
	Message string `json:"@message"`
	Type    string `json:"type"`
	Hook    struct {
		Resource struct {
			Addr string `json:"addr"`
			Type string `json:"resource_type"`
		} `json:"resource"`
		Action  string  `json:"action"`
		ID      string  `json:"id_value"`
		Elapsed float64 `json:"elapsed_seconds"`
	} `json:"hook"`
}

var statuses = map[string]Status{
	"apply_start":    Started,
	"apply_complete": Done,
	"apply_errored":  Failed,
}

func (w *Writer) line(line []byte) error {
	var m message
	if json.Unmarshal(line, &m) != nil || m.Message == "" && m.Type == "" {
		_, err := w.W.Write(append(line, '\n'))
		return err
	}
	if status, ok := statuses[m.Type]; ok && w.Report != nil && m.Hook.Resource.Addr != "" {
		w.Report(Resource{
			Addr: m.Hook.Resource.Addr, Type: m.Hook.Resource.Type, ID: m.Hook.ID, Action: m.Hook.Action, Status: status,
			Elapsed: time.Duration(m.Hook.Elapsed * float64(time.Second)),
		})
	}
	if m.Message == "" {
		return nil
	}
	_, err := io.WriteString(w.W, m.Message+"\n")
	return err
}
