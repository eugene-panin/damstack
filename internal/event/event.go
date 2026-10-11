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

// RunStarted: a run of steps begins, such as a deploy. Steps are all of them,
// in order, with how long each takes as its stack says ("" when it does not).
type RunStarted struct {
	Command string
	Project string
	// Stack is the platform's stack and its version, such as "hashi v0.21.0";
	// Server the address of its server.
	Stack  string
	Server string
	Steps  []PlannedStep
}

// PlannedStep is a step a run is going to take.
type PlannedStep struct {
	Step
	Takes string
}

// StepStarted: a step begins. Takes is how long its stack says it takes,
// empty when it does not say. Kind is what runs it (ansible, tofu, run) and
// What its playbook or directory; Log is where its tools' output goes, empty
// when to the terminal.
type StepStarted struct {
	Step
	Takes string
	Kind  string
	What  string
	Log   string
}

// TaskPlan: the tasks the step running now is expected to go through, in
// order (the resources its plan changes, or the tasks its play had last
// time), so the ones to come can be shown.
type TaskPlan struct {
	Names []string
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

func (RunStarted) event()  {}
func (TaskPlan) event()    {}
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

// RunDone: a run ended well. Links are the addresses what it set up serves;
// Next the commands worth running after it.
type RunDone struct {
	Links []Link
	Next  []NextStep
}

// Link is an address a project serves. Admin is an admin page, reached through
// the private network; Token names the token that signs in to it, if any.
type Link struct {
	URL   string
	Admin bool
	Token string
}

// NextStep is a command worth running next, and why.
type NextStep struct {
	Command string
	Why     string
}

func (RunDone) event() {}
