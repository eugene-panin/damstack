# Backlog: apps that update themselves

Recorded 2026-10-12. Not designed yet: a later session works it out.

## The idea

CI of an app repo builds, tests and publishes the images. It never touches a
server. damstack watches for a new build and deploys it.

- CI (e.g. GitHub Actions in twinmind-pro/sauron): tests, `linux/amd64`
  images, push to the registry. Only a green build moves the branch's channel
  tag (e.g. `:main`) to the new image.
- damstack tracks a branch per app: it resolves the channel tag to a digest,
  compares it with the digest in `stack.yaml`, and when they differ writes the
  new digests and deploys that app only.
- It runs on a schedule (launchd, like `backup pull` with `pull_every`), so the
  laptop has to be on with the project's tunnel up (the deploy takes the
  Consul lock).
- Safety: deploy on its own only when the plan changes nothing but images.
  Anything else (volumes, policies, env) stops and tells the person.

## Where apps come from

- Public stacks (mail, Traefik, NAS, …) from public repos and the library.
- Private apps (e.g. Sauron) added through the TUI, with the branch to follow.

## Rejected

CI deploying itself (a WireGuard peer for CI, a Nomad token from Vault through
GitHub OIDC, image tags in Consul KV): the person rejected it. Only damstack
deploys to production.

## Open

- The manifest field (something like `images.follow: <branch>`); a new field
  needs a damstack release and a raised `requires.damstack`.
- Which branch is followed, and whether each project can choose its own.
- The command's name (`damstack update`?).
