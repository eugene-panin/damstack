package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/eugene-panin/damstack/internal/toolbox"
)

// sshAccess sets how the tools image logs in with key: the key itself when it
// has no passphrase, the ssh-agent of this machine otherwise.
func sshAccess(ctx context.Context, r *toolbox.Runner, key string) error {
	if exec.CommandContext(ctx, "ssh-keygen", "-y", "-P", "", "-f", key).Run() == nil {
		r.Key = key
		return nil
	}
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return fmt.Errorf("%s has a passphrase and no ssh-agent runs; start one and add the key: ssh-add %s", key, key)
	}
	r.Agent = "/run/host-services/ssh-auth.sock"
	if runtime.GOOS == "linux" {
		out, _ := exec.CommandContext(ctx, "docker", "info", "--format", "{{.OperatingSystem}}").Output()
		if !strings.Contains(string(out), "Docker Desktop") {
			r.Agent = sock
		}
	}
	err := r.Run(ctx, toolbox.Cmd{Args: []string{"ssh-add", "-l"}, Quiet: true})
	var exit *toolbox.ExitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &exit) && exit.Code == 1:
		return fmt.Errorf("%s has a passphrase and your ssh-agent does not hold it; add it: ssh-add %s", key, key)
	case errors.As(err, &exit) && exit.Code == 2:
		return errors.New("the tools image cannot reach your ssh-agent; with Colima, start it with colima start --ssh-agent")
	}
	return err
}
