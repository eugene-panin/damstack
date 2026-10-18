# damstack.yaml

Every field of the manifest of a stack, for whoever writes one. `damstack
stack lint` checks a manifest against all of this and names the line of each
problem; [stacks.md](stacks.md) says how damstack runs a stack.

Templates below are Go `text/template` over `.config`, the project's
`stack.yaml`, with the functions `secret "name"` (a secret of `vault.yml`),
`firstHost "10.77.0.0/24"`, `join ", " .list`, `yaml .value` and
`freeSubnet`. A missing key is an error, not an empty string.

## The stack

| Field | What it is |
|---|---|
| `apiVersion` | `damstack/v2`. A `damstack/v1` stack ran in Docker and is refused. |
| `name` | The stack's name, 2 to 31 lowercase letters, digits and hyphens; its repository is `damstack-<name>`. |
| `description` | One line, shown where a stack is picked and in the catalog of apps. |
| `kind` | `platform`, the default, `app` or `domain`: an app runs on the platform of a project, and a domain is a module that connects the person's domain to it, such as by its public DNS records. |
| `needs` | What the person must have before starting, said first by deploy. |
| `takes` | About how long a first deploy takes. |
| `summary` | Lines shown before a project is set up: `{label, value}`, the value a template over the answers. |
| `done` | Lines said when a deploy ends, templates over `.config` (and `.app` for an app): addresses, how to sign in. An address under `.service.consul`, or under the `infra_domain` of `stack.yaml`, is an admin page, reached over the tunnel; when its first label is a name of `tokens`, such as `nomad.service.consul`, that token goes with it. |
| `tokens` | The secrets `damstack token <short name>` gives, by a short name. |
| `requires.damstack` | A version constraint on damstack, such as `">=0.18.0"`. |
| `requires.toolbox` | A version constraint on damstack-toolbox. |
| `requires.host` | Checks of this Mac before a deploy: `ssh-key`, `wireguard`. |

A platform also has:

| Field | What it is |
|---|---|
| `provides` | The [capabilities](#capabilities) it gives its apps, such as `nomad@1` and `vault-kv@1`. |
| `app_env` | The environment of every step of its apps, templates over its `stack.yaml` and secrets: how they reach what it provides, such as `NOMAD_ADDR` and `NOMAD_TOKEN`. With `CONSUL_HTTP_ADDR` in it, a project can be shared: its lock and OpenTofu state live on the platform's Consul. |

An app also has, and so does a domain:

| Field | What it is |
|---|---|
| `requires.provides` | The capabilities it needs of the platform, all of them. They decide whether it goes on a platform at all; no other module is brought for them. |
| `requires.capabilities` | The capabilities it needs of any module of the project, the platform or another app: `damstack app add` brings a provider of each one missing, or stops. Each is a capability or `{capability, when}`. |
| `uses` | The capabilities it connects to when the project has a provider, and works without; each as in `requires.capabilities`. |
| `provides` | The capabilities it gives the other apps of its project, such as `metrics@1`. |
| `targets.<name>` | How it runs on a platform that provides `<name>`: its own `steps` and `commands`. The first target, by name, the platform provides is used; a name without `@` is the capability at major 1. |
| `accounts` | Sign-ins of the app damstack can give new passwords: `{what, userpass}`, `userpass` the path of a userpass method of the platform's Vault, a template over `.config` and `.app`. |

An app may not have `server` or `backup`: those are the platform's.

A domain is checked as an app, goes on a platform the same way, by its
`requires.provides` and `targets`, and provides what it gives, such as
`public-dns@1`; `damstack app add` adds it, the plan brings it for a
capability it provides like any app, and `project.yaml` keeps it under `apps:`.
Wherever this page says an app, it means a domain too.

## Capabilities

A capability is what a module gives or needs, as `name@major`: the name in
lowercase letters, digits and hyphens, and the major version of what it
promises, such as `metrics@1` or `postgres@16`. A name without `@`, as
manifests wrote them before, is `@1`: `nomad` is `nomad@1`.

An item of `requires.capabilities` or `uses` holds only under a condition
when it is written `{capability: postgres@16, when: postgres.mode=external}`.
`when` is as for a question: the name of a `bool` question answered yes, or
`name=value`; the name may also be a dotted path of the app's settings, such
as `postgres.mode`. It is read over the app's own answers and the settings
they render into its block of `stack.yaml`, and on a deploy over that block.

`damstack app add` works out a plan before it asks anything: for each
capability the app requires that no module of the project provides, the app
of the catalog for the project's platform that provides it; one question when
several do, the one the catalog marks `recommended` first; a stop, naming the
capability, when none does. The same for what those require, in turn. One
under a condition is settled after the questions of its app. The modules are
added in order, each after what it requires, then deployed together.

For what an app uses, damstack shows the providers there are and asks
whether to connect them, and records the answer under `connect` of the app's
block, such as `connect: {metrics@1: [mimir]}`; a provider the catalog has but
the project does not is only named.

A provider is found in the platform's Consul, when its `app_env` has
`CONSUL_HTTP_ADDR`: its service carries the tag `damstack.provides=metrics@1`
(or `damstack.provides=metrics` for a major 1), with what else a client needs
in the service's meta, such as `path`. When Consul does not answer, the
providers are the modules of `project.yaml`, by name only.

Every step of an app that requires or uses capabilities gets the providers it
is given, those it requires and those of its `connect`, as JSON in
`TF_VAR_damstack_providers`, and its templates as `.providers`:

```json
{"metrics@1": [{"name": "mimir", "address": "10.77.0.1", "port": 9009, "meta": {"path": "/prometheus"}}]}
```

The registry's `index.yaml` gives each app its `provides`, `requires` (its
`requires.capabilities`), `uses` and `recommended`, so that a plan finds
providers without fetching every app.

## Questions

`questions` are asked on the first deploy; `config` is the template that
renders the answers into `stack.yaml`. Without a terminal the answers come
from `--answers`.

| Field | What it is |
|---|---|
| `name` | The answer's name in the template. `project` is taken: damstack asks it itself. |
| `prompt` | The question. |
| `type` | `string` (the default), `bool`, `list` or `enum`. |
| `default` | The answer taken on enter; a string may be a template over the answers before it. |
| `options` | The choices of an `enum`; its default must be one of them. |
| `pattern` | A regular expression a `string` answer, or each item of a `list`, must match. |
| `checks` | Checks run on the answer; one that fails asks again: `ssh-port`, `{cloudflare-token: <field>}`. |
| `section`, `note` | A heading the question goes under, and a line said before it. |
| `advanced` | Not asked: the default is taken, and `stack.yaml` changes it later. |
| `when` | Asked only if an earlier answer holds: the name of a `bool` question answered yes, or `name=value` of an `enum` or `string` one. |

## Secrets

`secrets` are generated or asked for once and kept in the encrypted
`vault.yml`. Every secret of an app begins with the app's name and `_`
(hyphens as underscores), so the secrets of apps and of the platform never
meet.

| Field | What it is |
|---|---|
| `name` | The key in `vault.yml`, and for `secret "name"` in templates. |
| `generate` | `password`, `hex`, `base64`, `uuid` or `ca`. Exactly one of `generate` and `ask`. |
| `bytes` | How many random bytes for `hex` and `base64`. |
| `cert` | For a generated `ca`, where its certificate goes in the project; the key is the secret. The CA signs for the private network only. |
| `ask` | The question for a secret the person gives, such as an API token. |
| `new_at` | For an asked secret, the `https://` page where a new value is made; the secrets screen opens it. |
| `optional` | An asked secret that may stay unset: the app works with less. |
| `checks` | As for questions. |
| `when` | As for questions. |
| `rotate` | A generated secret `damstack secret rotate` may make anew; the next deploy puts it in place. |
| `keep` | Why a generated secret cannot be made anew, such as `old backups need it`. Not with `rotate`. |
| `env`, `for` | For an app: the environment variable its services get the secret as, and the services that get it (all when empty). Such a value reaches them through Vault, never `stack.yaml` or the OpenTofu state. |

## The server

`server` is how damstack reaches the server of a platform; each field but
`wireguard` and `ssh_key` is a template over `stack.yaml`.

| Field | What it is |
|---|---|
| `address` | The public address. |
| `first_user` | The user the provider gives, with a password: damstack puts the SSH key there with `ssh-copy-id` on the first deploy. |
| `ops_user` | The user the steps log in as once the server is set up. |
| `tunnel` | The address over the private network; steps marked `tunnel` wait until port 22 answers there. |
| `host_key` | The SHA256 fingerprint of the server's host key, as the provider shows it, such as `SHA256:abc…`. Given, the first login checks it and stops on another; without it, damstack shows the key it gets and asks on a terminal, and takes it as it is without one. |
| `tunnel_help` | How to turn the tunnel on, said when a step needs it and finds it off. |
| `tunnel_configs` | A pattern of the devices' WireGuard configurations in the project, shown then. |
| `wireguard` | The keys of the private network damstack keeps: `secret` (where in `vault.yml`), `devices` (a dotted path of the list of devices in `stack.yaml`), `network`, `endpoint` (templates), `apply` (the command that sets the keys on the server), `interface` (`wg0` by default), `dns` (see below). |
| `ssh_key` | An SSH key of the project for when the person has none: `secret`, and `apply`, the command that authorizes it on the server. |

`server.wireguard.dns: true` says the server answers DNS on port 53 at its
address on `network`, the first one, such as Consul for the names under
`.consul`. The configurations of the devices then have `DNS = <that address>`
in `[Interface]`, and `damstack trust` writes `/etc/resolver/consul` on the
Mac, with sudo, so that the Mac asks the server for those names only:

```
# damstack shop /Users/me/.damstack/shop
nameserver 10.77.0.1
port 53
```

Only one project's tunnel answers for `.consul` at a time: damstack names the
project whose resolver is there, and asks before replacing it. `damstack trust
--remove` takes it back while it is still the project's. After a deploy,
damstack offers it once, and again when the address moved. Without `dns`, the
default, the configurations have no DNS and nothing is written.

## Backups

`backup` is where the server keeps its restic backups and where damstack
pulls them on this Mac, as templates over `stack.yaml`.

| Field | What it is |
|---|---|
| `from` | A path on the server, reached over SFTP as `ops_user` through `tunnel`; for a stack without a server, a path in the project. |
| `to` | A path on this Mac, under the home directory with `~/`, or in the project. |
| `password` | The password of the repository on the server, such as `'{{ secret "backup_password" }}'`. The copy on this Mac has a password of its own, `damstack_backup_copy_password` in `vault.yml`, made on the first pull. |
| `keep` | How many snapshots stay after a pull, by restic's names: `last`, `hourly`, `daily`, `weekly`, `monthly`, `yearly`. |
| `every` | How often a pull runs by itself, such as `1h` or `30m`, or `off`. |

A pull copies only the server's snapshots newer than the latest one here and
not dated later than now, then checks the copy and forgets what `keep` does
not keep.

## Steps and commands

`steps` run in order on every deploy; `commands` run by name, as `damstack
<command>`, or `damstack <app> <command>` for an app. A command may not take a
name of damstack's own.

| Field | What it is |
|---|---|
| `name` | 2 to 31 lowercase letters, digits and hyphens, once per list. |
| `title`, `takes` | What it does, for people, and about how long. |
| `ansible` | A playbook: `playbook`, `inventory` (a directory), `requirements` (collections to install first). |
| `tofu` | An OpenTofu directory: `dir`, `action` (`apply`, `plan` or `output`), `policy: {dir, nomad_jobs}` (Conftest policies run on the plan, and on the Nomad jobs it submits), `outputs` (output name to a file of the project, written as JSON after an apply). |
| `run` | A program of the stack, with its arguments. |
| `env` | Environment variables of the step, templates over `.config` (and `.app` for an app). |
| `confirm` | Ask before applying; a plan that replaces Nomad allocations asks anyway. |
| `once` | Run until it succeeds once; `damstack restore` runs it again. |
| `tunnel` | Wait until `server.tunnel` answers first. |
| `after_apps` | For a platform: run after the steps of its apps, such as publishing the DNS records of all of them. |
| `restore` | Run only in `damstack restore`, which gives every step the latest backup as a tar in `DAMSTACK_RESTORE`. |
| `keep` | Move values a step left in a JSON file of the project into `vault.yml`, then remove the file: `{file, secrets: {secret: field}}`. For an app, each secret begins with the app's name. |

A step runs exactly one of `ansible`, `tofu` and `run`. Two `tofu` steps whose
directories end in the same name are refused: their state would be one.

An Ansible step whose inputs did not change since it last ran well is
skipped: the stack's Ansible, `stack.yaml` but its apps, the platform's
secrets and damstack itself. It runs anyway once a week, and with `damstack
deploy --all`.

## Check

`check` is how `damstack stack check` proves the stack without a server.

| Field | What it is |
|---|---|
| `answers` | The answers a test project is set up from. |
| `platform` | For an app, the `stack.yaml` of a platform it is checked on. |
| `steps` | Steps run on the test project; what they ask is granted. |
