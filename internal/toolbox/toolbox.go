// Package toolbox runs commands in damstack-toolbox, with the stack mounted
// read-only at /stack and the project at /work.
package toolbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	StackDir     = "/stack"
	ProjectDir   = "/work"
	PasswordFile = "/run/damstack/vault/vault-pass"
	KeyFile      = "/home/damstack/.ssh/damstack-key"
	AgentSocket  = "/run/damstack/ssh-agent.sock"
)

// keyScript writes the SSH key from the environment into the container, which
// goes with it: a key mounted as a single file is root's on some Docker
// hosts, such as Colima.
const keyScript = `umask 077
if [ -n "${DAMSTACK_SSH_KEY_DATA:-}" ]; then
  mkdir -p "$HOME/.ssh"
  printf '%s\n' "$DAMSTACK_SSH_KEY_DATA" >"$HOME/.ssh/damstack-key"
fi
unset DAMSTACK_SSH_KEY_DATA
umask 022
exec "$@"`

type Runner struct {
	Image string
	// Stack, Project, Password and Key are paths on this machine. Password is
	// a file named vault-pass, whose directory is mounted; Key, the private
	// SSH key, may be empty.
	Stack     string
	Project   string
	Password  string
	Key       string
	PublicKey string
	// Agent is the socket of an ssh-agent on the Docker host, used instead of
	// Key for a key with a passphrase. Docker Desktop gives it to containers
	// as root's group, so the user of the container joins that group.
	Agent    string
	UID, GID int
	// TTY gives commands a terminal, for colors and prompts.
	TTY    bool
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type Cmd struct {
	Args []string
	Env  map[string]string
	// Stdout takes the output instead of the runner's, without a terminal.
	Stdout io.Writer
	// Quiet keeps the output unless the command fails.
	Quiet bool
}

// ExitError is a command that ran and failed.
type ExitError struct {
	Name string
	Code int
}

func (e *ExitError) Error() string { return fmt.Sprintf("%s failed with exit code %d", e.Name, e.Code) }

// Env is what every command sees, besides its own.
func (r *Runner) Env() map[string]string {
	env := map[string]string{
		"DAMSTACK_STACK":               StackDir,
		"DAMSTACK_PROJECT":             ProjectDir,
		"DAMSTACK_VAULT_PASSWORD_FILE": PasswordFile,
		"DAMSTACK_SSH_PUBLIC_KEY":      r.PublicKey,
	}
	if r.Key != "" {
		env["DAMSTACK_SSH_KEY"] = KeyFile
	}
	if r.Agent != "" {
		env["SSH_AUTH_SOCK"] = AgentSocket
	}
	return env
}

// Args are the arguments of docker run for c. Values of the environment are
// not among them, so that secrets do not show in the process list: docker
// takes them from its own environment, which Run sets.
func (r *Runner) Args(c Cmd) []string {
	args := []string{"run", "--rm", "-i"}
	if r.TTY && c.Stdout == nil && !c.Quiet {
		args = append(args, "-t")
	}
	args = append(args,
		"--user", strconv.Itoa(r.UID)+":"+strconv.Itoa(r.GID),
		"-v", r.Stack+":"+StackDir+":ro",
		"-v", r.Project+":"+ProjectDir,
		"-v", filepath.Dir(r.Password)+":"+path.Dir(PasswordFile)+":ro",
	)
	if r.Agent != "" {
		args = append(args, "--group-add", "0", "-v", r.Agent+":"+AgentSocket)
	}
	for _, name := range r.names(c) {
		args = append(args, "-e", name)
	}
	if r.Key != "" {
		args = append(args, "-e", "DAMSTACK_SSH_KEY_DATA")
	}
	args = append(args, "-w", ProjectDir, r.Image, "sh", "-c", keyScript, "damstack")
	return append(args, c.Args...)
}

func (r *Runner) names(c Cmd) []string {
	var names []string
	for name := range r.Env() {
		names = append(names, name)
	}
	for name := range c.Env {
		names = append(names, name)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func (r *Runner) Run(ctx context.Context, c Cmd) error {
	cmd := exec.CommandContext(ctx, "docker", r.Args(c)...)
	cmd.Env = os.Environ()
	for name, value := range r.Env() {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	for name, value := range c.Env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	if r.Key != "" {
		key, err := os.ReadFile(r.Key)
		if err != nil {
			return err
		}
		cmd.Env = append(cmd.Env, "DAMSTACK_SSH_KEY_DATA="+strings.TrimSpace(string(key)))
	}
	cmd.Stdin = r.Stdin
	var captured bytes.Buffer
	switch {
	case c.Quiet:
		cmd.Stdin = nil
		cmd.Stdout, cmd.Stderr = &captured, &captured
	case c.Stdout != nil:
		cmd.Stdin = nil
		cmd.Stdout, cmd.Stderr = c.Stdout, r.Stderr
	default:
		cmd.Stdout, cmd.Stderr = r.Stdout, r.Stderr
	}
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if c.Quiet {
			r.Stderr.Write(captured.Bytes())
		}
		if exit.ExitCode() == 125 {
			return &ExitError{Name: "docker run of the tools image", Code: 125}
		}
		return &ExitError{Name: name(c.Args), Code: exit.ExitCode()}
	}
	return err
}

func name(args []string) string {
	if len(args) == 0 {
		return "the command"
	}
	return args[0][strings.LastIndex(args[0], "/")+1:]
}
