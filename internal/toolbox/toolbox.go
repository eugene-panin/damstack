// Package toolbox runs commands with damstack-toolbox, unpacked on this Mac:
// its OpenTofu, Ansible, Conftest and restic, in an environment of its own
// rather than the user's.
package toolbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Passthrough are the variables of the user a command sees: who and where
// the user is, the terminal, and the proxies the network may need.
var Passthrough = []string{"HOME", "USER", "LOGNAME", "TMPDIR", "TERM", "COLORTERM",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"}

type Runner struct {
	// Toolbox is the directory of the toolbox, Stack and Project those of
	// the stack and the project, Password the file of the vault password.
	Toolbox  string
	Stack    string
	Project  string
	Password string
	// Key is the private SSH key, empty when Agent, the socket of an
	// ssh-agent, holds it.
	Key       string
	Agent     string
	PublicKey string
	// Cache is where OpenTofu keeps the providers it fetched.
	Cache string
	// TTY gives commands the terminal, for colors and prompts.
	TTY    bool
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type Cmd struct {
	Args []string
	Env  map[string]string
	// Stdout takes the output instead of the runner's.
	Stdout io.Writer
	// Quiet keeps the output unless the command fails; Silent keeps it even
	// then.
	Quiet  bool
	Silent bool
}

// ExitError is a command that ran and failed.
type ExitError struct {
	Name string
	Code int
}

func (e *ExitError) Error() string { return fmt.Sprintf("%s failed with exit code %d", e.Name, e.Code) }

// Env is what every command of a stack sees from damstack, besides its own.
func (r *Runner) Env() map[string]string {
	env := map[string]string{
		"DAMSTACK_STACK":               r.Stack,
		"DAMSTACK_PROJECT":             r.Project,
		"DAMSTACK_VAULT_PASSWORD_FILE": r.Password,
		"DAMSTACK_SSH_PUBLIC_KEY":      r.PublicKey,
	}
	if r.Key != "" {
		env["DAMSTACK_SSH_KEY"] = r.Key
	}
	return env
}

// Environ is the whole environment of a command: nothing of the user's but
// Passthrough, the toolbox first on the PATH, its configurations, what
// damstack says, and the command's own, in that order of precedence.
func (r *Runner) Environ(c Cmd) []string {
	env := map[string]string{}
	for _, name := range Passthrough {
		if v, ok := os.LookupEnv(name); ok {
			env[name] = v
		}
	}
	if env["TMPDIR"] == "" {
		env["TMPDIR"] = os.TempDir()
	}
	env["LANG"] = "en_US.UTF-8"
	env["PATH"] = filepath.Join(r.Toolbox, "bin") + ":/usr/bin:/bin:/usr/sbin:/sbin"
	env["ANSIBLE_CONFIG"] = filepath.Join(r.Toolbox, "etc", "ansible.cfg")
	env["ANSIBLE_HOME"] = filepath.Join(r.Project, ".damstack", "work", "ansible")
	// The sockets of ssh's ControlMaster must fit in 104 bytes on macOS.
	env["ANSIBLE_SSH_CONTROL_PATH_DIR"] = filepath.Join(env["TMPDIR"], "damstack-cp")
	env["TF_CLI_CONFIG_FILE"] = filepath.Join(r.Toolbox, "etc", "tofurc")
	if r.Cache != "" {
		env["TF_PLUGIN_CACHE_DIR"] = filepath.Join(r.Cache, "tofu-plugins")
	}
	if r.Agent != "" {
		env["SSH_AUTH_SOCK"] = r.Agent
	}
	for k, v := range r.Env() {
		env[k] = v
	}
	for k, v := range c.Env {
		env[k] = v
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

// Path is the program a command runs: one of the toolbox by its name, else
// one of the system, or the path given.
func (r *Runner) Path(name string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	for _, dir := range []string{filepath.Join(r.Toolbox, "bin"), "/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s is neither in the toolbox nor on this Mac", name)
}

func (r *Runner) Run(ctx context.Context, c Cmd) error {
	if len(c.Args) == 0 {
		return errors.New("no command to run")
	}
	program, err := r.Path(c.Args[0])
	if err != nil {
		return err
	}
	if r.Cache != "" {
		if err := os.MkdirAll(filepath.Join(r.Cache, "tofu-plugins"), 0o755); err != nil {
			return err
		}
	}
	cmd := exec.CommandContext(ctx, program, c.Args[1:]...)
	cmd.Env = r.Environ(c)
	cmd.Dir = r.Project
	var captured bytes.Buffer
	switch {
	case c.Quiet:
		cmd.Stdout, cmd.Stderr = &captured, &captured
	case c.Stdout != nil:
		cmd.Stdout, cmd.Stderr = c.Stdout, r.Stderr
	default:
		cmd.Stdin = r.Stdin
		cmd.Stdout, cmd.Stderr = r.Stdout, r.Stderr
	}
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if c.Quiet && !c.Silent {
			r.Stderr.Write(captured.Bytes())
		}
		return &ExitError{Name: filepath.Base(c.Args[0]), Code: exit.ExitCode()}
	}
	return err
}
