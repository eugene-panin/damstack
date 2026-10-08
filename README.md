# damstack

English | [Русский](README.ru.md) | [Español](README.es.md)

Your own server, set up and kept running from your Mac with one program.

You rent a server, answer a few questions, and damstack turns it into a
private platform: locked down to your key, reachable through your own
WireGuard network, with Nomad, Consul and Vault to run apps, certificates
from Let's Encrypt, and nightly backups copied to your Mac. Then you add apps
to it, such as your own mail server, with one command each.

You need to know nothing about servers. damstack asks what it needs, says
what it does, and checks that the server stays the way you set it up.

## What you need

- A Mac, Apple silicon or Intel, with [Homebrew](https://brew.sh).
- The [WireGuard app](https://apps.apple.com/app/wireguard/id1451685025), for
  the private network between your devices and the server.
- A server with Ubuntu 24.04, from any provider: its IP address and the root
  password the provider gives you. 2 CPUs, 4 GB of memory and 20 GB of disk
  are enough.
- A domain whose DNS is on [Cloudflare](https://www.cloudflare.com), and an
  API token there that may edit the DNS of that domain. Other DNS providers
  work too, by the name [lego](https://go-acme.github.io/lego/dns/) gives
  them.
- For mail: a provider that lets the server send on port 25. Many block it
  until you ask.

## Install

```bash
brew install eugene-panin/tap/damstack
damstack
```

The first run checks your Mac and says what is missing and how to get it.
damstack downloads the tools it runs, OpenTofu, Ansible and restic, once, and
keeps them apart from anything else on your Mac. No Docker is needed.

## Your first server

```bash
damstack deploy hashi
```

damstack asks for the address of the server, the domain of the admin pages
(such as `admin.example.com`), your email for Let's Encrypt, the Cloudflare
token, and the devices that may reach the server, such as `laptop` and
`phone`. Then, once, the root password of the server, to put your SSH key
there; it is not kept.

Then it runs, in about ten minutes:

1. **Securing the server**: a user of your own, logins with your key only,
   root and passwords off, a firewall, automatic security updates.
2. **The platform**: the WireGuard network, Consul, Vault and Nomad, Docker,
   a synchronized clock, and the nightly backup.
3. **Traefik and the admin pages**: a certificate for `*.<your domain>`,
   pages that open only through WireGuard.
4. **DNS records**, published for you.

Halfway, it stops to bring your Mac into the private network:

```bash
damstack tunnel show laptop
```

puts the configuration into the WireGuard app; on a phone,
`damstack tunnel show phone --qr` shows a code to scan.

At the end it lists the admin pages, `nomad.`, `vault.` and
`consul.<your domain>`, and how to get their tokens:

```bash
damstack token nomad
```

`damstack trust` makes your Mac trust the certificates of the private
network, so that the servers also open on their own addresses.

## Apps

```bash
damstack app add mail
damstack deploy
```

asks what the app needs and runs it on your server. Today the library has one
app: **mail**, your own mail server with [Stalwart](https://stalw.art):
mailboxes for every domain, DKIM, MTA-STS, IMAP and SMTP with certificates,
and the DNS records mail clients find their settings from.
`damstack mail output passwords` shows the passwords of the mailboxes.

Apps run in containers on Nomad, each with only the rights it needs.

## Every day

| You want to | Run |
| --- | --- |
| See the projects and what needs attention | `damstack` |
| Check that the server is as you set it up | `damstack status --live` |
| Change a setting | `damstack edit`, then `damstack deploy` |
| Deploy again, changing only what differs | `damstack deploy` |
| Add or remove a device of the private network | `damstack tunnel add phone`, `damstack tunnel remove phone` |
| Give a device or the server new keys | `damstack tunnel rotate phone` |
| Open a shell on the server, or run a command there | `damstack ssh`, `damstack ssh -- uptime` |
| See everything damstack ran | `damstack history` |
| Rename a project on your Mac | `damstack rename old new` |

With several projects, name the one you mean, as in `damstack status ovh`,
or make one current with `damstack use ovh`.

## Backups

The server backs up every night: the data of Consul, Vault and Nomad, and the
data of every app. damstack copies the backups to your Mac every hour while
it is on; `damstack backup status` shows them, and the home screen warns
when they get old.

Everything that brings a project back lives on your Mac. Make a **recovery
kit**, one encrypted file with the project and its password, and keep it
somewhere else, such as cloud storage, with its passphrase in your password
manager:

```bash
damstack backup kit
```

On another Mac, `damstack backup open <kit>` brings the project back.

## When the server is lost

Rent a new server, give its address to the project, and restore:

```bash
damstack edit          # the new address under server
damstack restore
```

damstack sets the new server up as it did the first one, then puts back
Consul, Vault and the data of every app from the latest backup on your Mac.
Your keys, tokens, mailboxes and mail are as they were.

## Where things are

A project is a directory, `~/.damstack/<name>`:

- `stack.yaml`, the one file to edit; `damstack edit` opens it and checks it;
- `vault.yml`, the secrets, encrypted; the password to it is in
  `~/.config/damstack/projects/<name>`, outside the project;
- the state of the platform and the history of what ran.

To remove damstack: stop the hourly copy of each project with
`launchctl bootout gui/$(id -u)/dev.damstack.backup.<name>` and delete
`~/Library/LaunchAgents/dev.damstack.backup.<name>.plist`; then
`brew uninstall damstack`, and delete `~/.damstack`, `~/.config/damstack` and
`~/.cache/damstack`. Keep the first two, or a recovery kit, as long as a
server of yours runs: without them nothing can manage it.

## Limits today

- macOS only, and one server per project.
- One app in the library: mail.
- New releases of a platform or an app are taken by hand; there is no
  `damstack upgrade` yet.
- Tested on OVH and Google Cloud.

## More

- [docs/stacks.md](docs/stacks.md): how a stack is written, and how to
  develop damstack.
- [docs/design.md](docs/design.md): where damstack is going.
- The platform: [damstack-hashi](https://github.com/eugene-panin/damstack-hashi);
  the mail app: [damstack-mail](https://github.com/eugene-panin/damstack-mail).

MIT license.
