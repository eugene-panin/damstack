# damstack

One program to deploy and run infrastructure stacks. A stack, such as
[hashi](https://github.com/eugene-panin/damstack-hashi) (Consul,
Vault and Nomad on one server over WireGuard), is a repository with a
`damstack.yaml` that says what to ask and which steps to run. damstack fetches
the stack, keeps the deployment in one directory described by `stack.yaml`,
and runs the steps with the tools of a small Docker image. You install
damstack and Docker, nothing else.

Work in progress.

```bash
damstack               # on the first run, checks this machine, then shows the commands
damstack doctor        # checks this machine, and says how to get what it lacks
damstack stacks        # the stacks damstack can deploy
damstack add owner/name   # github.com/owner/damstack-name, or any git address
damstack deploy        # asks the questions of a stack, sets up a project, deploys it
damstack status        # in a project: its stack, and how each step went last
damstack history       # in a project: everything damstack ran on it
damstack stack lint    # checks the damstack.yaml of a stack
damstack stack check   # proves a stack without a server
```

## A project

`damstack deploy` asks the questions of the stack and sets up a project in
`~/damstack/<name>`, or where `--dir` says:

- `stack.yaml`: the one file to edit, rendered from the answers;
- `vault.yml`: the secrets, generated or asked for, encrypted with Ansible
  Vault; the password is in `~/.config/damstack/projects/<name>/vault-pass`,
  outside the project, so the project can go to git;
- files the stack generates, such as a CA certificate, and `state/`, the
  OpenTofu state;
- `.damstack/`: the stack release the project is deployed with, the history,
  and `work/`, what runs leave, not kept in git.

Then it runs the steps of the stack in damstack-toolbox, with the stack
mounted read-only at `/stack` and the project at `/work`. Before the first
step it makes sure your SSH key logs in to the server, and if it does not,
puts it there with `ssh-copy-id` and the password your provider gave. A step
marked `once` runs until it succeeds once; a step marked `confirm` asks before
it changes anything. `damstack deploy` in a project deploys it again.

Inside a project, the commands of its stack are damstack commands too, such as
`damstack output`.

## Writing a stack

A stack lives in a git repository named `damstack-<name>`, where `<name>` is
the `name` in its `damstack.yaml`, such as `damstack-hashi` for `hashi`; then
`damstack add owner/<name>` finds it on GitHub. Releases are tags such as
`v0.1.0`.

`damstack.yaml` declares the questions, the secrets, the template of
`stack.yaml`, and the steps, each one of an Ansible playbook, an OpenTofu
directory with its policies, or a program of the stack. `damstack stack lint`
checks it, `damstack stack check` sets up a project from the stack's test
answers and checks the playbooks, OpenTofu and the policies, and
`damstack deploy --from <dir>` deploys the stack as it is in a directory.
`test/demo` is a small stack that uses every kind of step.

## Development

```bash
go test -race ./...
go build ./cmd/damstack
test/e2e.sh    # deploys test/demo with the real tools image
```
