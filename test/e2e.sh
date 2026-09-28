#!/usr/bin/env bash
# damstack end to end with the demo stack in test/demo and the real tools
# image: set up, deploy, deploy again, a policy that denies, a no to apply.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$HOME/.cache"
work=$(mktemp -d "$HOME/.cache/damstack-e2e.XXXXXX")
trap 'rm -rf "$work"' EXIT
export XDG_CONFIG_HOME=$work/config XDG_CACHE_HOME=$work/cache

damstack=$work/damstack
(cd "$repo" && go build -o "$damstack" ./cmd/damstack)
stack=$repo/test/demo
project=$work/demo1

step() { printf '\n== %s\n' "$*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
deploy() { "$damstack" deploy --from "$stack" "$@" >"$work/out" 2>&1; }

step "stack check proves the demo stack"
"$damstack" stack check "$stack" >"$work/out" 2>&1 || { cat "$work/out"; fail "stack check"; }

step "a new project: questions, secrets, and every step"
cd "$work"
printf '\nlaptop, phone\nn\ntok\ny\ny\n' | deploy --name demo1 --dir "$project" || { cat "$work/out"; fail "first deploy"; }
cd "$project"
[[ $(cat greeting.txt) == "hello, laptop and phone" ]] || fail "the playbook did not write the greeting"
head -1 vault.yml | grep -q '^\$ANSIBLE_VAULT;1.1;AES256' || fail "vault.yml is not encrypted"
grep -q 'root_token\|tok' vault.yml && fail "a secret is in vault.yml in the clear"
[[ -e .damstack/work/init.json ]] && fail "the kept file was left"
grep -q 'BEGIN CERTIFICATE' ca.pem || fail "no CA certificate"
[[ $(stat -f %Lp "$XDG_CONFIG_HOME/damstack/projects/demo1/vault-pass" 2>/dev/null || stat -c %a "$XDG_CONFIG_HOME/damstack/projects/demo1/vault-pass") == 600 ]] ||
  fail "the vault password is readable by others"
[[ $("$damstack" output greeting) == '"hello"' ]] || fail "the output command"

step "a second deploy changes nothing"
deploy </dev/null || { cat "$work/out"; fail "second deploy"; }
grep -q 'init: done before' "$work/out" || fail "a once step ran again"
grep -q 'changed=0' "$work/out" || { cat "$work/out"; fail "the playbook changed something"; }
grep -q 'Nothing to change' "$work/out" || { cat "$work/out"; fail "OpenTofu had changes"; }

step "a plan the policy denies is not applied"
sed -i.bak 's/^greeting: hello/greeting: bye/' stack.yaml
deploy </dev/null && fail "the denied plan went through"
grep -q 'the policy forbids' "$work/out" || { cat "$work/out"; fail "no policy message"; }
grep -q 'breaks a rule of the stack' "$work/out" || { cat "$work/out"; fail "no explanation"; }
[[ $("$damstack" output greeting) == '"hello"' ]] || fail "the denied plan was applied"

step "a no to apply changes nothing"
sed -i.bak 's/^greeting: bye/greeting: hi/' stack.yaml
printf 'n\n' | deploy && fail "a no went through"
grep -q 'you said no' "$work/out" || { cat "$work/out"; fail "no explanation"; }
[[ $("$damstack" output greeting) == '"hello"' ]] || fail "applied after a no"

step "status and history"
"$damstack" status | grep -Eq '^apply +.* failed' || fail "status does not show the failed apply"
[[ $("$damstack" history | grep -c ' deploy ') -ge 9 ]] || fail "history is short"

printf '\nall end to end scenarios passed\n'
