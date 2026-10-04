# damstack design

What damstack is, how a person uses it, what exists and what is next. The
screens are the target; `Now` says how far the code is from each.

## Model

- **damstack** is one program, for macOS. It checks the Mac, keeps a library
  of stacks, asks the questions of a stack, and runs its steps with
  damstack-toolbox: Ansible on a Python of its own, OpenTofu, Conftest and
  restic, one archive per kind of Mac that damstack downloads once, checks
  against the SHA-256 it was built with, and runs in an environment of its
  own, apart from the user's shell and tools. The person installs damstack,
  nothing else.
- **A stack** is a git repository `damstack-<name>` with a `damstack.yaml`:
  its questions, secrets, the template of `stack.yaml`, and its steps. The
  manifest is the contract between the stack and damstack.
- **A platform** is a stack that sets up a server: hashi (Nomad, Consul,
  Vault), later a plain Linux one and a light Kubernetes. It says what it
  `provides`, such as `nomad@1`, and in `app_env` how apps reach it.
- **An app** is a stack that runs on a platform, such as mail. It `requires`
  what a platform provides and has one target per kind of platform.
- **Building blocks** are Ansible collections and OpenTofu modules in their
  own repositories. A stack assembles them, ours or anyone's.
- **A project** is a directory, `~/.damstack/<name>`: `stack.yaml`, the one
  file to edit, `vault.yml` with the secrets, OpenTofu state, and in
  `.damstack/` the releases deployed and the history. The vault password is
  outside it, in `~/.config/damstack/projects/<name>/vault-pass`.

The library is curated. Platforms are ours; apps are written against the
contract of a platform and tested on it. A stack of one's own still deploys
with `--from` or its address, for people who write stacks, with no promise.

HashiCorp tools are under the BSL: people run them on their own servers,
which it allows. damstack earns nothing and offers no hosted service.

## First run

```
$ damstack

damstack sets up your own server and runs apps on it, from one file you edit.
It runs on this Mac and brings its own tools; you install nothing else.

This machine
  ok    macOS
  ok    git version 2.50.1 (Apple Git-155)
  ok    the toolbox 1.1.0 is downloaded on first use, about 90 MB
  ok    SSH key ~/.ssh/id_ed25519
  ok    WireGuard app, for the admin pages of your server

Platforms
  hashi   Nomad, Consul and Vault on one server, admin pages behind WireGuard
          apps for it: mail

Start with
  damstack deploy hashi
  It asks a few questions and sets up a project in ~/.damstack/<name>.
  You need: a server with Ubuntu 24.04 and its root password, and a domain.

damstack help lists every command.
```

When something is missing, what to fix comes first and the platforms after.
Every later `damstack` without arguments is a home screen: the projects, their
platform, apps and last deploy; the machine is checked again only when
something broke.

Now: done, in 0.1.0. The library comes from
[damstack-library](https://github.com/eugene-panin/damstack-library), at most
once an hour.

## Setting up a platform

```
$ damstack deploy hashi

hashi: Nomad, Consul and Vault on one server; admin pages behind WireGuard.

You will need, about 20 minutes, and:
  • a server with Ubuntu 24.04, its IP address and root password
  • a domain on Cloudflare, and a token for it (damstack says how to make one)

A name for this project, such as my-cloud: my-cloud

── Server
IP address of the server: 203.0.113.10
  ok  it answers on port 22

── Admin pages
They open only on your devices, through WireGuard.
Domain for them, such as admin.example.com: admin.example.com
Is example.com on Cloudflare? [Y/n]
Cloudflare token (Zone DNS Edit and Zone Read on example.com): ****
  ok  the token sees example.com
Your email, for Let's Encrypt about the certificates: me@example.com

── Your devices
Devices that may open the admin pages [laptop]: laptop, phone

Summary
  project   ~/.damstack/my-cloud
  server    203.0.113.10, you log in as ops afterwards
  admin     consul., nomad., vault.admin.example.com
Set it up? [Y/n]

1/5  Your key on the server
     Type the root password your provider gave you; it is not kept.     ok
2/5  Securing the server (1 min)             ok  root and passwords are off
3/5  Consul, Vault, Nomad, WireGuard (6 min) ok
     ─ Your devices join the private network ─
     laptop: import ~/.damstack/my-cloud/clients/laptop.conf into WireGuard, turn it on
     phone:  scan this code in the WireGuard app  [QR]
     Press Enter when the laptop is on…      ok  the server answers through WireGuard
4/5  Traefik and the admin pages (1 min)     12 things to create. Go? [Y/n]  ok
5/5  DNS records (10 s)                      1 record in example.com. Go? [Y/n]  ok

Done. my-cloud is running.
  https://nomad.admin.example.com    token: damstack token nomad
  https://vault.admin.example.com    token: damstack token vault
  https://consul.admin.example.com   token: damstack token consul
Next: damstack app add mail
```

What the manifest needs for it:

- question sections, and advanced questions that take their default and are
  not asked (bootstrap user, ops user); the network interface is read on the
  server after the first login;
- checks of an answer, from a set damstack keeps: `ssh-port`,
  `cloudflare-token`, `dns-zone`, `dns-existing`, `not-admin-domain`;
- a title and a time for every step; the full output goes to a log of the
  project, and to the terminal with `--verbose`;
- a short summary of a plan to confirm (`12 things to create`), the plan
  itself with `damstack plan`;
- the ending of a platform: the addresses it serves, and `damstack token`.

Now: done, in 0.1.0, except the first login as a numbered step; the network
interface is `auto`, the interface of the server's default route. Not yet run
on a real server as a whole.

## Adding an app

```
$ damstack app add mail

mail: your own mail server, by Stalwart, for your domains.

── Domains
Domains to receive mail for: example.com
  ok    example.com is on Cloudflare: its records go in by themselves
  warn  example.com receives mail today at mx.zoho.eu. After the deploy it comes
        here instead. Move your mailboxes first, or take another domain. Go on? [y/N]
Name of the mail server [mail.example.com]:
  ok    not among the admin pages (admin.example.com)
Mailboxes every domain gets [info]: info, me

Added mail to my-cloud: its settings are under apps.mail in stack.yaml.
Deploy now? [Y/n]
  ── The server
  ok    mail can leave: port 25 out is open
  warn  203.0.113.10 has no reverse name. Set it to mail.example.com in your
        provider's panel, or Gmail and Outlook put your mail in spam.
  1/3  Platform                  nothing to change
  2/3  Mail server (2 min)       7 things to create. Go? [Y/n]
  3/3  DNS records               14 records in example.com. Go? [Y/n]

Done.
  IMAP mail.example.com:993, SMTP mail.example.com:465 and 587;
  mail clients find these settings by themselves.
  Mailboxes: info@example.com, me@example.com. Passwords: damstack mail passwords
```

Every app has its own questions; damstack asks them the same way for all.
Checks are kinds damstack keeps, which a manifest names, never code of the
app:

```yaml
questions:
  - name: domains
    type: list
    checks: [dns-zone, {dns-existing: MX}]
  - name: hostname
    default: "mail.{{ index .domains 0 }}"
    checks: [not-admin-domain]
preflight:
  - {port-out: 25}
  - {reverse-name: "{{ .hostname }}"}
```

Checks of answers run at `app add`. Preflight checks need the server, so they
run at deploy, right before the steps of the app. DNS is looked up over
DNS-over-HTTPS, not the local resolver.

Outside a project, `app add` says the app runs on a platform, and offers to
set one up with the app. In a project whose platform is not deployed yet, the
app is recorded and deploys after the platform.

Now: apps add, deploy after the platform with their own state and the
platform's `app_env`, their DNS records published by the platform. No checks;
an existing MX elsewhere is not noticed and a second one is added; the mail
server's name under the admin domain is refused only by the policy of the DNS
step.

## Changing stack.yaml

```
$ damstack deploy

Changes since the last deploy (Sep 29):
  network.clients      + phone
  backup.schedule      03:00 → 04:30
```

- a copy of `stack.yaml` is kept at every successful deploy; `damstack diff`
  shows the changes since without deploying;
- a stack names its settings that cannot change, such as
  `immutable: [server.ops_user, network.cidr]`, and damstack refuses a change
  to them before any step;
- the keys of `stack.yaml` are checked before any step, so a misspelt key is
  not silently ignored.

Now: none of it; renaming the ops user is refused by the bootstrap playbook.

## Upgrading

```
$ damstack upgrade

hashi  v0.1.0 → v0.2.0
  • Nomad 2.0.7 → 2.1.0
  • new: backups also go to any S3 storage (backup.offsite, off by default)
mail   v0.1.0 → v0.1.1

A new setting of hashi v0.2.0:
  Keep a copy of the backups off the server too? [y/N]

Before the upgrade a backup is taken on the server.
Upgrade and deploy? [Y/n]
```

- what changed comes from the CHANGELOG of the stack;
- only the questions new in the release are asked, their answers go into
  `stack.yaml`, new secrets are generated;
- an app that needs `nomad@1` blocks a platform that now provides `nomad@2`,
  with a word on which release of the app to take first;
- rolling back is setting the release back in `project.yaml`; it is safe only
  when the new release changed nothing for good, hence the backup first;
- damstack itself upgrades with brew; the toolbox release is pinned to it.

Now: none of it; a project stays at the release it was set up with.

## Backups

```
$ damstack backup status
  on the server   /var/backups/restic     last: today 03:00
  on this laptop  ~/Backups/my-cloud      last: 2 days ago, pulled hourly
  off the server  —                       not set up (backup.offsite)
```

- a copy on the server is not a backup; the laptop copy is the main one,
  restic fetched by damstack on the host, a schedule with launchd or systemd;
- doctor and the home screen warn when the laptop copy is older than three
  days;
- nothing restores without the project directory and the vault password;
  damstack says so when it sets the project up, and asks once whether the
  password is kept somewhere;
- `damstack restore` sets up a new server from a backup: the platform, the
  snapshots of Consul, Nomad and Vault, and the data of the apps. Each stack
  declares how it restores, and a test on a real server deploys, backs up,
  destroys and restores.

Now: hashi backs up on the server every night; `damstack backup pull`
copies the snapshots to the laptop over SFTP through the tunnel, and forgets
what `backup.keep` of the stack does not keep; deploy schedules it with
launchd or systemd as `backup.every` says; `damstack backup status` and the
home screen warn when the laptop copy is older than two days or missing.
`damstack backup kit` packs the project and its vault password into one
file, encrypted with age under a passphrase it shows once; `damstack backup
open` brings the project back from it on another computer, and the home
screen asks for a new kit after the project changes. The server copies the
host volumes by name before restic runs, stopping the jobs with the meta
`backup = "stop"`, such as the mail server, for the copy. Restoring a server
is not there yet.

## Open questions, and the plan for each

**Removing an app or a project.** `damstack app remove mail` shows what goes
(the job, its data volumes, DNS records, ports), offers a backup of the app's
data, asks for the app's name typed back, then destroys its state (a new
OpenTofu action, `destroy`, which lifts the data policy explicitly), removes
`apps.mail` and its secrets, and the DNS step removes its records.
`damstack destroy` removes everything the apps and the platform made in Nomad,
Vault and DNS; the server itself is the provider's to delete, and the project
and its vault password stay, damstack says where. A stack lists the data a
removal loses, for the screen that asks.

**Each project its own private network.** A new project takes a free /24 of
10.64.0.0/10: not used by another project of damstack, nor by a network of the
laptop. The default of a question can be a template, here `{{ freeSubnet }}`,
and an advanced question takes its default without being asked. Existing
projects keep theirs; `immutable` refuses a change on a live server. Test: two
projects with both tunnels on, both deploy.

**Admin pages without a domain.** A third answer to the question of the
domain: none. Traefik then serves a certificate of the project's own CA for
names such as `nomad.my-cloud.internal`, which Consul DNS answers through the
tunnel. The person trusts `ca.pem` once; damstack adds it to the keychain or
the trust store when allowed, or says how. The `traefik` module takes a CA
instead of ACME; hashi gets the branch.

**DNS publishing beyond Cloudflare.** DNS modules move out of
terraform-nomad-hashistack into repositories of their own with the same
inputs, `records` and `zones`, and output, `zone_ids`: Cloudflare first, then
Hetzner, OVH, Route53 when someone needs them. The DNS step picks one by
`dns.provider`; `manual` stays.

**Server providers.** A provider in damstack creates a server, reinstalls
it, and gives its address and root password; `ssh`, a server one already has,
is the default. OVH comes from the ovhctl of ovh-stack-iac, then Hetzner. The
provider is the first question of deploy; its API token is kept in
`vault.yml`. Test: from no server to a running platform on Hetzner.

**More than one server.** Not now, but `server:` becomes `servers:`, a list
with roles, holding one server for now, so the format does not change after
1.0. The roles of hashi already form a cluster (the `cluster` scenario of the
collection); the work is in damstack, several logins and a tunnel to all, and
in the template. Test when it comes: three GCP servers.

## Order of work

1. First run and home screen; deploy with sections, checks of answers, the
   pause for WireGuard, steps with titles, the ending with `damstack token`;
   deploy choosing from the library or any address; each project its own
   private network.
2. Release: goreleaser, a tap, `brew install eugene-panin/tap/damstack`; the
   library as an index outside the binary.
3. Checks of `app add` and preflight: existing MX, port 25 out, reverse name,
   the mail server's name; removing an app or a project.
4. The copy of `stack.yaml`, `damstack diff`, immutable settings, checked keys;
   `servers:` and providers, with `ssh` the default, before 1.0.
5. Backups to the laptop, `backup status`, `restore`, and its test.
6. `damstack upgrade`, versioned contracts.
7. Admin pages without a domain; then Hetzner, DNS modules of their own, and
   a cluster, as they are needed.

## Exists

| Repository | What | Published |
|---|---|---|
| damstack | the program | GitHub release 0.1.0, `brew install eugene-panin/tap/damstack` |
| damstack-library | the platforms and apps damstack offers | GitHub |
| homebrew-tap | the cask of damstack, written by GoReleaser | GitHub |
| damstack-toolbox | the image steps run in | ghcr.io, 1.0.0 |
| damstack-hashi | the platform | GitHub, v0.2.0 |
| damstack-mail | the mail app | GitHub, v0.2.0 |
| ansible-collection-base | WireGuard, firewall, Docker, backup | Galaxy 0.5.0 |
| ansible-collection-hashistack | Consul, Vault with auto-unseal, Nomad | Galaxy 0.7.0 |
| terraform-nomad-hashistack | Traefik, workload identity, DNS on Cloudflare | registry 0.8.0 |
| terraform-nomad-stalwart | Stalwart on Nomad | registry 0.1.0 |

Tested end to end on a GCP server from scratch: a platform, the mail app, a
change of the admin domain, a reboot that unsealed Vault by itself, and a
second deploy that changed nothing.
