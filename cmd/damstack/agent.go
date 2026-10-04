package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/eugene-panin/damstack/internal/toolbox"
)

// sshAccess sets how the tools log in with key: the key itself when it has no
// passphrase, the ssh-agent of this Mac otherwise.
func sshAccess(ctx context.Context, r *toolbox.Runner, key string) error {
	if exec.CommandContext(ctx, "ssh-keygen", "-y", "-P", "", "-f", key).Run() == nil {
		r.Key = key
		return nil
	}
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return fmt.Errorf("%s has a passphrase and no ssh-agent runs; start one and add the key: ssh-add %s", key, key)
	}
	err := exec.CommandContext(ctx, "ssh-add", "-l").Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		r.Agent = sock
		return nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return fmt.Errorf("%s has a passphrase and your ssh-agent does not hold it; add it: ssh-add --apple-use-keychain %s", key, key)
	}
	return fmt.Errorf("your ssh-agent does not answer at %s: %w", sock, err)
}
