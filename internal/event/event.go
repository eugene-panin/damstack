// Package event is what damstack says while it works, as values: a step
// started, finished or failed, a note, a wait for the lock. Those who show it
// (the plain lines on a terminal or in CI, the TUI) are Sinks; the code that
// works never formats text for a screen itself.
package event

import "time"

// Event is one thing damstack says. The set is closed: only this package's
// types are Events.
type Event interface{ event() }

// Sink takes events as they happen. Emit may be called from another goroutine
// than the one that started the work, but never from two at once; it must not
// block the work for long.
type Sink interface {
	Emit(Event)
}

// SinkFunc makes a function a Sink.
type SinkFunc func(Event)

func (f SinkFunc) Emit(e Event) { f(e) }

// Discard is a Sink that drops every event.
var Discard Sink = SinkFunc(func(Event) {})

// Step names a step of a run: its place (N of Total), its name (an app's are
// app/step) and what it is called for people.
type Step struct {
	N, Total int
	Name     string
	Title    string
}

// StepStarted: a step begins. Takes is how long its stack says it takes,
// empty when it does not say.
type StepStarted struct {
	Step
	Takes string
}

// StepSkipped: a step is not run, and why.
type StepSkipped struct {
	Step
	Reason string
}

// StepDone: a step finished, in Took.
type StepDone struct {
	Step
	Took time.Duration
}

// StepFailed: a step failed with Err. Log is the file its tools wrote to
// (empty when their output went to the terminal), Tail its last lines.
type StepFailed struct {
	Step
	Err  error
	Log  string
	Tail []string
}

// Note is a line damstack says while working, such as "Nothing to change."
// Break sets it apart from what came before.
type Note struct {
	Text  string
	Break bool
}

// Heading starts a part of the output, such as one check of a stack.
type Heading struct {
	Text string
}

// Waiting: the project's lock is held by someone else. Holder says who, and
// what for; Ahead is how many are before this one, the holder among them.
type Waiting struct {
	Holder string
	Ahead  int
}

func (StepStarted) event() {}
func (StepSkipped) event() {}
func (StepDone) event()    {}
func (StepFailed) event()  {}
func (Note) event()        {}
func (Heading) event()     {}
func (Waiting) event()     {}

// TaskStatus is where a task of a step is.
type TaskStatus int

const (
	TaskRunning TaskStatus = iota
	TaskOK
	TaskChanged
	TaskFailed
	TaskSkipped
)

// Task: a piece of work inside the step running now (a resource OpenTofu
// changes, a task Ansible runs) changed status. Done of Total are finished;
// Estimate says Total is a guess that may grow (Ansible's includes); Total is 0
// when unknown.
type Task struct {
	Name     string
	Status   TaskStatus
	Done     int
	Total    int
	Estimate bool
	Took     time.Duration
	// Detail says more of where it is, such as "deployment running, 1 of 2 up".
	Detail string
}

func (Task) event() {}
