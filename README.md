# damstack

One program to deploy and run infrastructure stacks. A stack, such as
[hashi](https://github.com/eugene-panin/damstack-hashi) (Consul,
Vault and Nomad on one server over WireGuard), is a repository with a
`damstack.yaml` that says what to ask and which steps to run. damstack fetches
the stack, keeps the deployment in one directory described by `stack.yaml`,
and runs the steps with
[damstack-toolbox](https://github.com/eugene-panin/damstack-toolbox):
OpenTofu, Ansible on a Python of its own, Conftest and restic, which damstack
downloads once and runs apart from anything installed on the Mac. It runs on
macOS, Apple silicon and Intel; you install damstack, nothing else.

Work in progress: [docs/design.md](docs/design.md) says where it is going.

## Install

```bash
brew install eugene-panin/tap/damstack
damstack
```

It needs `git`, from the command line tools of Xcode, which Homebrew installs
too, and the WireGuard app for the private network of a server.

```bash
damstack               # on the first run, checks this machine, then shows the commands
damstack doctor        # checks this machine, and says how to get what it lacks
damstack stacks        # the stacks damstack can deploy
damstack add owner/name   # github.com/owner/damstack-name, or any git address
damstack deploy        # asks the questions of a stack, sets up a project, deploys it
damstack status        # in a project: its stack, and how each step went last
damstack history       # in a project: everything damstack ran on it; --json for scripts, as status
damstack app add mail  # in a project: add an app to its platform
damstack stack lint    # checks the damstack.yaml of a stack
damstack stack check   # proves a stack without a server
```

To remove it, `brew uninstall damstack`, then what it keeps on this Mac:

- `~/.config/damstack`: the vault password of each project. Without it the
  secrets of a project cannot be read, so keep it, or a recovery kit from
  `damstack backup kit`, as long as a server of the project runs;
- `~/.damstack`: the projects themselves, unless `--dir` put them elsewhere;
- `~/.cache/damstack`: the toolbox and the stacks, safe to delete;
- the backup pulls `damstack backup schedule` set up:
  `launchctl bootout gui/$(id -u)/dev.damstack.backup.<project>`, then delete
  `~/Library/LaunchAgents/dev.damstack.backup.<project>.plist` and
  `~/Library/Logs/damstack`.

## A project

`damstack deploy` asks the questions of the stack and sets up a project in
`~/.damstack/<name>`, or where `--dir` says:

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

Without a terminal, in a script or CI, damstack asks nothing: the answers and
the secrets come from the file `--answers` names, the name of a new project
from `--name`, and `--yes` goes ahead where it would ask, such as before a step
marked `confirm`. A question it would need fails at once and says which.

## Apps

A stack is a platform, such as hashi, or an app that runs on one, such as
mail. `damstack app add <app>` in a project asks the app's questions, puts its
settings under `apps.<app>` of the same `stack.yaml`, and its secrets in the
same `vault.yml`. `damstack deploy` then runs the steps of the platform, those
of every app, and last the platform's steps marked `after_apps`, such as the
one that publishes the DNS records the apps left in `dns/`. Each app keeps its
own OpenTofu state, and its commands are `damstack <app> <command>`.

A platform says what it `provides`, such as `nomad` and `vault-kv`, and in
`app_env` how its apps reach that. An app `requires` some of it, and has one
way to run, a target, per kind of platform: the first target the platform
provides is used, so an app runs on every platform of that kind, and another
kind only needs another target.

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
`test/demo` is a small platform that uses every kind of step, and
`test/demo-app` an app on it.

## Development

```bash
go test -race ./...
go build ./cmd/damstack
test/e2e.sh    # deploys test/demo with the real toolbox, from a poisoned shell too
```
