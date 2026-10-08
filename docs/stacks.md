# Stacks

A stack is what damstack deploys: a platform, such as
[hashi](https://github.com/eugene-panin/damstack-hashi), or an app that runs
on one, such as [mail](https://github.com/eugene-panin/damstack-mail). It is a
git repository with a `damstack.yaml` that says what to ask, which secrets to
make, and which steps to run.

## How damstack runs one

`damstack deploy` asks the questions of the stack and sets up a project in
`~/.damstack/<name>`, or where `--dir` says:

- `stack.yaml`, rendered from the answers by the template of the stack;
- `vault.yml`, the secrets, asked for or generated, encrypted with Ansible
  Vault; its password is in `~/.config/damstack/projects/<name>/vault-pass`;
- files the stack generates, such as a CA certificate, and `state/`, the
  encrypted OpenTofu state;
- `.damstack/`: the release of the stack the project is deployed with, the
  history, and `work/`, what runs leave.

Then it runs the steps with
[damstack-toolbox](https://github.com/eugene-panin/damstack-toolbox), the
native tools damstack downloads once: OpenTofu, Ansible on a Python of its
own, Conftest and restic. Each step gets an environment of its own, not the
user's shell. Before the first step damstack makes sure an SSH key logs in to
the server, and when none does, puts the user's key there with `ssh-copy-id`
and the password the provider gave.

- A step marked `once` runs until it succeeds once.
- A step marked `confirm` asks before it changes anything.
- A step marked `tunnel` waits until the server answers over WireGuard.
- A step marked `after_apps`, of a platform, runs after the steps of its apps.
- A step marked `restore` runs only in `damstack restore`, which also runs the
  `once` steps again, and gives every step the latest backup as a tar in
  `DAMSTACK_RESTORE`.
- `keep` moves values a step left in a file, such as the keys of a new Vault,
  into `vault.yml`.

Without a terminal, damstack asks nothing: the answers and secrets come from
`--answers`, the name from `--name`, and `--yes` goes ahead where it would
ask. A question it would need fails at once and says which.

## Apps

`damstack app add <app>` puts the app's settings under `apps.<app>` of the
same `stack.yaml`, and its secrets in the same `vault.yml`. A platform says
what it `provides`, such as `nomad` and `vault-kv`, and in `app_env` how its
apps reach that. An app `requires` some of it, and has a target per kind of
platform; the first target the platform provides is used. Each app keeps its
own OpenTofu state, and its commands are `damstack <app> <command>`.

## Writing one

A stack lives in a repository named `damstack-<name>`, where `<name>` is the
`name` in its `damstack.yaml`; `damstack add owner/<name>` finds it on
GitHub. Releases are tags such as `v0.1.0`.

`damstack.yaml` declares the questions, the secrets, the template of
`stack.yaml`, and the steps, each an Ansible playbook, an OpenTofu directory
with its policies, or a program of the stack.

```bash
damstack stack lint            # checks damstack.yaml
damstack stack check           # sets up a project from the test answers, checks playbooks, OpenTofu and policies
damstack deploy --from <dir>   # deploys the stack as it is in a directory
```

`test/demo` is a small platform that uses every kind of step, and
`test/demo-app` an app on it.

## Developing damstack

```bash
go test -race ./...
go build ./cmd/damstack
test/e2e.sh    # deploys test/demo with the real toolbox, from a poisoned shell too
```
