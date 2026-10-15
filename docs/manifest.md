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
| `kind` | `platform`, the default, or `app`: an app runs on the platform of a project. |
| `needs` | What the person must have before starting, said first by deploy. |
| `takes` | About how long a first deploy takes. |
| `summary` | Lines shown before a project is set up: `{label, value}`, the value a template over the answers. |
| `done` | Lines said when a deploy ends, templates over `.config` (and `.app` for an app): addresses, how to sign in. |
| `tokens` | The secrets `damstack token <short name>` gives, by a short name. |
| `requires.damstack` | A version constraint on damstack, such as `">=0.18.0"`. |
| `requires.toolbox` | A version constraint on damstack-toolbox. |
| `requires.host` | Checks of this Mac before a deploy: `ssh-key`, `wireguard`. |

A platform also has:

| Field | What it is |
|---|---|
| `provides` | What it gives its apps, such as `nomad` and `vault-kv`. |
| `app_env` | The environment of every step of its apps, templates over its `stack.yaml` and secrets: how they reach what it provides, such as `NOMAD_ADDR` and `NOMAD_TOKEN`. With `CONSUL_HTTP_ADDR` in it, a project can be shared: its lock and OpenTofu state live on the platform's Consul. |

An app also has:

| Field | What it is |
|---|---|
| `requires.provides` | What it needs of the platform, all of it. |
| `targets.<name>` | How it runs on a platform that provides `<name>`: its own `steps` and `commands`. The first target, by name, the platform provides is used. |
| `accounts` | Sign-ins of the app damstack can give new passwords: `{what, userpass}`, `userpass` the path of a userpass method of the platform's Vault, a template over `.config` and `.app`. |

An app may not have `server` or `backup`: those are the platform's.

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
| `wireguard` | The keys of the private network damstack keeps: `secret` (where in `vault.yml`), `devices` (a dotted path of the list of devices in `stack.yaml`), `network`, `endpoint` (templates), `apply` (the command that sets the keys on the server), `interface` (`wg0` by default). |
| `ssh_key` | An SSH key of the project for when the person has none: `secret`, and `apply`, the command that authorizes it on the server. |

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
