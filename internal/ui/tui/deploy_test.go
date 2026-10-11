package tui

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/eugene-panin/damstack/internal/event"
)

var update = flag.Bool("update", false, "write the golden frames anew")

var start = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

// midDeploy is the screen of the design's deploy: the platform half done, the
// provision play at task 31 of 72, the apps to come.
func midDeploy(width, height int) *Deploy {
	m := NewDeploy(Actions{})
	m.started, m.now = start, start
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	steps := []struct{ name, title, takes string }{
		{"bootstrap", "Securing the server", "1 min"},
		{"provision", "Consul, Vault, Nomad", "6 min"},
		{"apply", "Traefik and admin pages", "1 min"},
		{"postgres/apply", "postgres: The shared PostgreSQL", "2 min"},
		{"nats/apply", "nats: The shared NATS bus", "2 min"},
		{"seaweedfs/apply", "seaweedfs: The shared object lake", "2 min"},
		{"loki/apply", "loki: The shared log store", "2 min"},
		{"mimir/apply", "mimir: The shared metric store", "2 min"},
		{"grafana/apply", "grafana: Grafana", "2 min"},
		{"odoo/apply", "odoo: Odoo", "3 min"},
		{"sauron/apply", "sauron: Sauron", "5 min"},
		{"dns", "DNS records", "10 s"},
	}
	run := event.RunStarted{Command: "deploy", Project: "twinmind", Stack: "hashi v0.21.0", Server: "169.58.210.131"}
	for i, s := range steps {
		run.Steps = append(run.Steps, event.PlannedStep{Step: event.Step{N: i + 1, Total: len(steps), Name: s.name, Title: s.title}, Takes: s.takes})
	}
	send := func(e event.Event) { m.Update(eventMsg{e}) }
	send(run)
	at := func(n int) event.Step { return run.Steps[n-1].Step }
	send(event.StepStarted{Step: at(1), Kind: "ansible", What: "bootstrap.yml"})
	send(event.StepDone{Step: at(1), Took: 48 * time.Second})
	send(event.StepStarted{Step: at(2), Kind: "ansible", What: "provision.yml",
		Log: "/Users/eugene/.damstack/twinmind/.damstack/work/logs/2026-10-11T12-04-05-provision.log"})
	send(event.TaskPlan{Names: []string{"consul : install 2.0.4", "consul : extra config (connect)", "consul : grpc certificate",
		"consul : restart", "consul-cni : download 2.0.4", "consul-cni : link plugin", "nomad : agent config",
		"nomad : docker registry login", "nomad : restart if changed", "vault : service registration"}})
	tasks := []struct {
		name   string
		status event.TaskStatus
		took   time.Duration
	}{
		{"consul : install 2.0.4", event.TaskOK, 2100 * time.Millisecond},
		{"consul : extra config (connect)", event.TaskOK, 400 * time.Millisecond},
		{"consul : grpc certificate", event.TaskChanged, time.Second},
		{"consul : restart", event.TaskChanged, 3 * time.Second},
	}
	done := 26
	for _, t := range tasks {
		send(event.Task{Name: t.name, Status: event.TaskRunning, Done: done, Total: 72, Estimate: true})
		done++
		send(event.Task{Name: t.name, Status: t.status, Done: done, Total: 72, Estimate: true, Took: t.took})
	}
	m.now = start.Add(4*time.Minute + 12*time.Second)
	send(event.Task{Name: "consul-cni : download 2.0.4", Status: event.TaskRunning, Done: 31, Total: 72, Estimate: true})
	m.frame = 4
	m.now = m.now.Add(3400 * time.Millisecond)
	return m
}

func golden(t *testing.T, name, frame string) {
	t.Helper()
	path := filepath.Join("testdata", name+".txt")
	plainFrame := ansi.Strip(frame)
	if *update {
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, []byte(plainFrame), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to write it)", err)
	}
	if plainFrame != string(want) {
		t.Errorf("%s differs from %s:\n%s", name, path, plainFrame)
	}
}

// Every frame is exactly the window: as many rows, each as many cells. A row
// one cell too wide wraps and the whole screen slides.
func checkShape(t *testing.T, frame string, width, height int) {
	t.Helper()
	rows := strings.Split(frame, "\n")
	if len(rows) != height {
		t.Fatalf("%d×%d: %d rows", width, height, len(rows))
	}
	for i, r := range rows {
		if w := ansi.StringWidth(r); w != width {
			t.Errorf("%d×%d: row %d is %d cells: %q", width, height, i, w, ansi.Strip(r))
		}
	}
}

func TestTheDeployScreenAtTheSizesItIsMadeFor(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		w, h := size[0], size[1]
		frame := midDeploy(w, h).Frame()
		checkShape(t, frame, w, h)
		golden(t, fmt.Sprintf("deploy-%dx%d", w, h), frame)
	}
}

func TestAWindowTooSmallSaysSo(t *testing.T) {
	m := midDeploy(60, 15)
	frame := m.Frame()
	checkShape(t, frame, 60, 15)
	if !strings.Contains(ansi.Strip(frame), "needs a window of 80×20") {
		t.Errorf("got\n%s", ansi.Strip(frame))
	}
}

func TestResizingNeverLeavesARowTooWide(t *testing.T) {
	m := midDeploy(140, 40)
	for _, w := range []int{140, 80, 120, 97, 81, 200} {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
		checkShape(t, m.Frame(), w, 30)
	}
}

func TestAConfirmationIsAnsweredWithAKey(t *testing.T) {
	m := midDeploy(100, 30)
	answer := make(chan bool, 1)
	m.Update(confirmMsg{question: "sauron: 0 to add, 2 to change. Go?", answer: answer})
	if !strings.Contains(ansi.Strip(m.Frame()), "sauron: 0 to add, 2 to change. Go?") {
		t.Fatal("the question is not shown")
	}
	m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if !<-answer || m.confirm != nil {
		t.Error("y must answer yes and close the question")
	}
}

func TestQuitStopsAfterTheStepAndCtrlCTwiceAtOnce(t *testing.T) {
	stopped, aborted := false, false
	m := midDeploy(100, 30)
	m.actions = Actions{Stop: func() { stopped = true }, Abort: func() { aborted = true }}
	m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if !stopped || aborted || !strings.Contains(ansi.Strip(m.Frame()), "stopping after this step") {
		t.Fatalf("q must ask to stop after the step: stopped %v aborted %v", stopped, aborted)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !aborted || cmd == nil {
		t.Error("ctrl+c while stopping must stop at once")
	}
}

func TestAFailureStaysOnScreen(t *testing.T) {
	m := midDeploy(100, 30)
	m.Update(eventMsg{event.StepFailed{Step: m.run.Steps[1].Step, Err: errors.New("tofu failed"),
		Tail: []string{"ERROR! The requested handler 'restart nomad' was not found"}}})
	_, cmd := m.Update(finishMsg{err: errors.New("the step provision failed: ansible-playbook failed with exit code 2")})
	if cmd != nil {
		t.Error("a failed run must wait for a key, not quit")
	}
	frame := ansi.Strip(m.Frame())
	if !strings.Contains(frame, "Consul, Vault, Nomad failed") || !strings.Contains(frame, "restart nomad") {
		t.Errorf("got\n%s", frame)
	}
	checkShape(t, m.Frame(), 100, 30)
}

func TestAppsAreAListEachWhereItIs(t *testing.T) {
	m := midDeploy(100, 30)
	send := func(e event.Event) { m.Update(eventMsg{e}) }
	step := func(n int) event.Step { return m.run.Steps[n-1].Step }
	send(event.StepDone{Step: step(2), Took: 5*time.Minute + 2*time.Second})
	send(event.StepStarted{Step: step(3), Kind: "tofu", What: "infra"})
	send(event.StepDone{Step: step(3), Took: 6 * time.Second})
	for n := 4; n <= 8; n++ {
		send(event.StepStarted{Step: step(n), Kind: "tofu", What: "infra"})
		send(event.StepDone{Step: step(n), Took: 3 * time.Second})
	}
	send(event.StepStarted{Step: step(9), Kind: "tofu", What: "infra"})
	send(event.TaskPlan{Names: []string{"module.grafana.nomad_job.grafana", "module.grafana.vault_kv_secret_v2.admin"}})
	send(event.Task{Name: "module.grafana.nomad_job.grafana", Status: event.TaskRunning, Done: 0, Total: 2})
	frame := m.Frame()
	checkShape(t, frame, 100, 30)
	golden(t, "deploy-apps-100x30", frame)
}
