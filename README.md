# hashistack

One program to run your own server as a small private cloud. It keeps the
server described in one file, `stack.yaml`, and does the rest with the tools
it brings along in a Docker image: you install hashistack and Docker, nothing
else.

Work in progress. What it does today:

```bash
hashistack            # on the first run, checks this machine, then shows the commands
hashistack doctor     # checks this machine, and says how to get what it lacks
hashistack version
```

`doctor` checks that Docker is installed, running, recent and has enough
memory, that the tools image is downloaded or can be, that there is an SSH
key and, if it has a passphrase, an ssh-agent holding it, and that WireGuard
is installed. Inside a project, a directory with a `stack.yaml`, it also checks
the vault password, the Cloudflare tokens and that the server answers through
WireGuard.

## Development

```bash
go test -race ./...
go build ./cmd/hashistack
```
