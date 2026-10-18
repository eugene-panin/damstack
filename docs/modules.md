# Modules, projects and what they need of each other

The design of damstack's next step: what a person builds from the registry,
how damstack knows what goes with what, and the first run of someone who has
only a server. It extends the model of [design.md](design.md); where the two
differ, this one is the target.

## Modules

Everything damstack sets up is a **module**: a directory of the
[registry](https://github.com/damstack-dev/damstack-registry) with its
`damstack.yaml`, released by a tag of its own (`hashi/v0.9.0`,
`mail/v0.4.0`). Every module has the same parts: questions, secrets, steps
(Ansible, OpenTofu, a program), what it `provides` and what it `requires`.
They differ by `kind`:

| Kind | Goes on | Example | Provides | Requires |
|---|---|---|---|---|
| `platform` | a host | hashi, later k0s | `nomad@1`, `vault-kv@1`, `consul-dns@1` | a host damstack logs in to |
| `domain` | a platform | cloudflare, manual | `public-dns@1` | the platform's ingress |
| `app` | a platform | traefik, postgres, mail, odoo | what it serves, such as `ingress-public@1` or `postgres@16` | what it runs on and needs |

In the registry, each kind has its directory: `platforms/<name>`,
`domains/<name>`, `apps/<name>`.

A module that serves other modules is still an app: Traefik, a shared
PostgreSQL or Loki run on the platform like any other, and say what they
provide.

## Capabilities

A module says what it needs as **capabilities**, never as names of other
modules: an app requires `ingress-public@1`, not Traefik. A capability is a
word and a major version, `name@major`; a module that breaks what a
capability promised provides the next major.

Two reasons:

- one module can take the place of another: Caddy for Traefik, a managed
  database for the shared PostgreSQL, with no change to the apps;
- what an app needs is said exactly. Mail needs public ports (25, 465, 993)
  and a certificate for its domain, not an HTTP proxy: it requires
  `public-ports@1` and `tls-certificates@1`, and runs without Traefik.

A module needs a capability in one of two ways:

| Field | Means | Example |
|---|---|---|
| `requires` | it does not work without it: the plan brings a provider, or stops | mail: `public-ports@1`, `tls-certificates@1` |
| `uses` | it connects to every provider it finds, and works without one | grafana: `metrics@1`, `logs@1` |

Either may hold only under a setting, with the same `when` as a question:
`{capability: postgres@16, when: postgres_mode=external}` for odoo, whose
own database is the default.

The capabilities of today's modules:

| Module | Provides |
|---|---|
| hashi | `nomad@1`, `vault-kv@1`, `consul@1`, `ingress-traefik@1`, `ingress-public@1`, `observability@1` |
| loki | `logs@1` |
| mimir | `metrics@1` |
| postgres | `postgres@16` |
| nats | `nats@2` |
| seaweedfs | `s3@1` |

A capability written without a version, as manifests of today do (`nomad`),
reads as `@1`.

The capabilities in use are listed in the registry's README, with what each
promises; a new one is added there in the same pull request as the first
module that provides it.

## Finding providers

A provider says how to reach it, so that what uses it never names it: a
module's service registers in Consul with the tag
`damstack.provides=<capability>`, and what else a client needs in the
service's meta, such as `path=/prometheus`. Grafana does not know Mimir; it
knows `metrics@1`, and takes the address and path from Consul.

When a module that `uses` or `requires` a capability is added, damstack asks
the platform's Consul which services carry that tag, and offers them:

```
grafana can show what these give:
  mimir   metrics    mimir.service.consul:9009/prometheus
  loki    logs       loki.service.consul:3100
Connect them? [Y/n]
```

So a provider damstack did not set up, such as a Prometheus put there by
hand, is found once its service carries the tag. When the platform's Consul
does not answer, damstack takes the providers of `project.yaml` and says it
could not look on the platform. A platform without Consul finds providers
its own way, such as service labels on Kubernetes; the manifests stay the
same.

## Projects

A **project** is what one person builds: one platform on their host, the
domains they connect and the apps they deliver, each at a release. It lives
where its person keeps it, `~/.damstack/<name>`, and is theirs: damstack
never keeps it anywhere else.

`project.yaml` records the modules, each pinned; this is where it goes:

```yaml
name: shop
platform: {name: hashi, url: https://github.com/damstack-dev/damstack-registry, path: platforms/hashi, tag: hashi/v1.0.0, commit: …}
domains:
  - {name: cloudflare, url: …, path: domains/cloudflare, tag: cloudflare/v0.1.0, commit: …}
apps:
  - {name: traefik, url: …, path: apps/traefik, tag: traefik/v0.1.0, commit: …}
  - {name: odoo, url: …, path: apps/odoo, tag: odoo/v0.4.0, commit: …}
```

A `project.yaml` of today, with `stack:` and `apps:`, is read as before:
`stack` is the platform, and nothing is moved. Until `platform` and
`domains` come, a domain module is kept under `apps:` like an app, its kind
read from its manifest.

## What damstack works out

When a person adds a module, damstack builds the plan before it asks
anything:

1. Take what the module requires. Each capability is either provided by a
   module of the project already, or missing.
2. For each one missing, find the modules of the registry that provide it and
   can go on this platform. One: it joins the plan. Several: one question,
   the registry's recommended one first, each said by what it gives the
   person ("Cloudflare: damstack adds the records" / "another provider: you
   add them by hand"), not by its name alone. None: stop, and say what is
   missing.
3. Repeat for what those modules require, until nothing is missing.
4. Order the plan so that every module comes after what it requires.
5. Show it as one screen, then ask the questions of each module in that
   order, then deploy them in that order.

```
odoo needs a public address with TLS:
  traefik      the public entry, with its certificates      new
  cloudflare   your domain example.com on Cloudflare        new
  odoo         at shop.example.com                          new
Go?
```

Removing goes the other way: a module others require is not removed while
they are there, and damstack says which.

An app supports a platform when it has a target for one of the capabilities
the platform provides. The targets of an app are a list in its order of
preference, not a map, so that the first is the one it prefers.

## The platform needs no domain

A platform lives on its private network. Everything on it has a name there,
from Consul's DNS: `nomad.service.consul`, `vault.service.consul`, and each
service an app adds. The person's devices reach that network over WireGuard.
The project's CA already signs only such names (`.consul`, `.nomad`,
`localhost` and private addresses), so its certificates are good for exactly
that network, and `damstack trust` makes the Mac trust them.

For the Mac to find `.consul` names, damstack writes
`/etc/resolver/consul`, which sends only those queries to Consul through the
tunnel, with the same `sudo` as `damstack trust`. A phone gets Consul's
address as the DNS of its WireGuard configuration; Consul forwards what is
not its own.

So setting up a platform asks for the project's name, the host, and the
devices that may reach it. No domain, no e-mail for Let's Encrypt, no DNS
token.

## Domains

A domain module connects the person's domain to the project: it publishes
the records the project's apps need, from what they say they need, and
provides `public-dns@1`. The certificates of public apps are not its: Traefik
(`apps/traefik`) gets them from Let's Encrypt by HTTP-01, once the records
point at the server.

A domain module goes on a platform like an app, with the same targets,
questions, secrets and steps, and is added with `damstack app add`.

A provider damstack supports natively is a module with deterministic OpenTofu,
Cloudflare first. For any other, the `manual` module shows the records to add
by hand, and checks they are there.

## The first run

A person installed damstack and has a server with Ubuntu 24.04 they reach by
SSH, with the root password or a key from their provider.

1. `damstack` checks the Mac, then asks what to set up: the apps of the
   registry by what they give, mail, Odoo, a website, or a bare platform.
2. They pick one. damstack picks the platform: the one their project has
   already; else the only one the app supports; else one question, the
   registry's recommended platform first.
3. The host: its address, and the root password if their key does not log
   in yet. damstack shows the host key and asks whether the provider shows
   the same.
4. The plan, with the domain only if the app needs a public address. The
   questions of each module.
5. One deploy of the whole plan.
6. What they have now, and how to open it: the WireGuard configuration (a QR
   code for the phone), `damstack trust`, the addresses.

The next app starts at step 4.

## What changes

In the registry:

- `domains/` beside `platforms/` and `apps/`;
- hashi without what is the domain's: its questions for the domain, e-mail,
  DNS provider, zones and tokens, the admin pages under `infra_domain`, the
  Let's Encrypt certificate, the `dns` step. Its admin pages get `.consul`
  names. This is hashi `v1.0.0`;
- Traefik out of hashi into `apps/traefik`, providing `ingress-public@1`;
- `domains/cloudflare` out of hashi's `dns` step, providing `public-dns@1`;
  `domains/manual` new; the certificates of public apps with Traefik, by
  HTTP-01;
- the apps require capabilities with a major version, and list their targets
  in order;
- `index.yaml` marks the recommended platform and, per capability, the
  recommended module.

In damstack:

- `kind: domain`; `requires` of capabilities across modules, not only of the
  platform; targets as a list;
- the plan: resolving capabilities over the project and the registry,
  ordering, the screen, deploying in order, refusing to remove what others
  need;
- `project.yaml` with `platform`, `domains` and `apps`, reading today's form
  as before;
- `/etc/resolver/consul` beside `damstack trust`;
- the first run starting from what to set up, not from the platform.

## Projects of today

Projects pinned to hashi before `v1.0.0` keep their domain, their Traefik in
hashi and their certificates: those releases do not change. Moving one to
hashi `v1.0.0` is an upgrade with steps of its own, as Traefik and the
records move from the platform to their modules without a gap; it is
written, and tried on a test project, before anyone is asked to do it.

## Open questions

- A capability's version: only a major, or ranges like `postgres@>=15`?
  Majors first; ranges when a module needs one.
- Several modules providing one capability in one project, such as two
  PostgreSQL: allowed, with the app picking which by name in its settings?
  Not at first.
- Hosts with several servers, for k0s: a platform with more than one host
  changes step 3 of the first run.
- Checking a host can carry a platform (memory, disk, ports) before the plan.

## Order of work

1. The capability names, `requires` and `uses` with `when`, and the plan in
   damstack, on today's modules: loki, mimir, postgres, nats and seaweedfs
   say what they provide; odoo requires `postgres@16` when external; grafana
   uses `metrics@1` and `logs@1`. Their services carry the
   `damstack.provides` tag, and damstack finds providers in Consul.
2. Apps require `ingress-public@1`, not Traefik by name; hashi provides it with
   the Traefik it runs, so nothing changes on a server.
3. hashi `v1.0.0`: without a domain and without Traefik, `.consul` names and
   `/etc/resolver/consul`; Traefik as `apps/traefik`, providing
   `ingress-public@1`; `domains/cloudflare` and `domains/manual`. Traefik
   leaves hashi in the same release it joins the apps, so that no server
   runs two.
4. The first run from what to set up.
5. The upgrade of a project of today to hashi `v1.0.0`, tried on a test
   project, then written for others.
