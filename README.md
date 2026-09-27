# damstack

One program to deploy and run infrastructure stacks. A stack, such as
[hashistack](https://github.com/eugene-panin/hashistack-starter) (Consul,
Vault and Nomad on one server over WireGuard), is a repository with a
`damstack.yaml` that says what to ask and which steps to run. damstack fetches
the stack, keeps the deployment in one directory described by `stack.yaml`,
and runs the steps with the tools of a small Docker image. You install
damstack and Docker, nothing else.

Work in progress. What it does today:

```bash
damstack            # on the first run, checks this machine, then shows the commands
damstack doctor     # checks this machine, and says how to get what it lacks
damstack version
```

`doctor` checks that Docker is installed, running, recent and has enough
memory, that the tools image is downloaded or can be, that there is an SSH key
and, if it has a passphrase, an ssh-agent holding it, and that WireGuard is
installed. Inside a project, a directory with a `stack.yaml`, it also checks
the vault password, the Cloudflare tokens and that the server answers through
WireGuard.

## Development

```bash
go test -race ./...
go build ./cmd/damstack
```
