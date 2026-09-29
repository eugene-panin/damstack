package toolbox

import (
	"slices"
	"strings"
	"testing"
)

func TestArgsKeepSecretsOutOfTheCommandLine(t *testing.T) {
	r := &Runner{Image: "img:1", Stack: "/s", Project: "/p", Password: "/c/projects/demo/vault-pass", Key: "/h/.ssh/id_ed25519",
		PublicKey: "ssh-ed25519 AAAA", UID: 501, GID: 20, TTY: true}
	args := r.Args(Cmd{Args: []string{"tofu", "plan"}, Env: map[string]string{"CLOUDFLARE_API_TOKEN": "s3cret"}})
	line := strings.Join(args, " ")
	for _, want := range []string{
		"run --rm -i -t --user 501:20 -v /s:/stack:ro -v /p:/work -v /c/projects/demo:/run/damstack/vault:ro",
		"-e CLOUDFLARE_API_TOKEN -e DAMSTACK_PROJECT", "-e DAMSTACK_SSH_KEY_DATA -w /work img:1 sh -c",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("no %q in\n%s", want, line)
		}
	}
	if strings.Contains(line, "s3cret") || strings.Contains(line, "ssh-ed25519") {
		t.Errorf("a secret is on the command line:\n%s", line)
	}
	if !slices.Equal(args[len(args)-3:], []string{"damstack", "tofu", "plan"}) {
		t.Errorf("the command is not last: %v", args[len(args)-3:])
	}
	if quiet := strings.Join(r.Args(Cmd{Args: []string{"x"}, Quiet: true}), " "); strings.Contains(quiet, " -t ") {
		t.Errorf("a quiet command got a terminal: %s", quiet)
	}
}

func TestAgent(t *testing.T) {
	r := &Runner{Image: "img:1", Stack: "/s", Project: "/p", Password: "/c/vault-pass", Agent: "/run/host-services/ssh-auth.sock", UID: 501, GID: 20}
	line := strings.Join(r.Args(Cmd{Args: []string{"true"}}), " ")
	if !strings.Contains(line, "--group-add 0 -v /run/host-services/ssh-auth.sock:/run/damstack/ssh-agent.sock") ||
		strings.Contains(line, "-e DAMSTACK_SSH_KEY_DATA") || r.Env()["SSH_AUTH_SOCK"] != AgentSocket || r.Env()["DAMSTACK_SSH_KEY"] != "" {
		t.Errorf("args %s, env %v", line, r.Env())
	}
}
