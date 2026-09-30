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
deploy() { "$damstack" deploy --verbose "$@" >"$work/out" 2>&1; }

step "stack check proves the demo stack"
"$damstack" stack check "$stack" >"$work/out" 2>&1 || { cat "$work/out"; fail "stack check"; }

step "a new project: questions, secrets, and every step"
cd "$work"
printf '\nlaptop, phone\nn\ntok\ny\ny\n' | deploy --from "$stack" --name demo1 --dir "$project" || { cat "$work/out"; fail "first deploy"; }
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

step "without --verbose the steps are numbered and the tools write to a log"
"$damstack" deploy </dev/null >"$work/out" 2>&1 || { cat "$work/out"; fail "a brief deploy"; }
grep -Eq '^2/4  configure$' "$work/out" || { cat "$work/out"; fail "the steps are not numbered"; }
grep -q 'PLAY RECAP' "$work/out" && fail "the output of Ansible is on the terminal"
grep -q 'changed=0' .damstack/work/logs/*-configure.log || fail "the log has no output of Ansible"
[[ $(grep -c '     ok, ' "$work/out") -eq 3 ]] || { cat "$work/out"; fail "the steps did not say ok"; }

step "a plan the policy denies is not applied"
sed -i.bak 's/^greeting: hello/greeting: bye/' stack.yaml
"$damstack" deploy </dev/null >"$work/out" 2>&1 && fail "the denied plan went through"
grep -q '  | FAIL .*the policy forbids' "$work/out" || { cat "$work/out"; fail "the reason is not shown from the log"; }
grep -q 'The whole output is in .*-apply.log' "$work/out" || { cat "$work/out"; fail "no path to the log"; }
grep -q 'breaks a rule of the stack' "$work/out" || { cat "$work/out"; fail "no explanation"; }
[[ $("$damstack" output greeting) == '"hello"' ]] || fail "the denied plan was applied"

step "a no to apply changes nothing, asked by the counts of the plan"
sed -i.bak 's/^greeting: bye/greeting: hi/' stack.yaml
printf 'n\n' | "$damstack" deploy >"$work/out" 2>&1 && fail "a no went through"
grep -q '1 to change. Go?' "$work/out" || { cat "$work/out"; fail "the question does not count the changes"; }
grep -q 'you said no' "$work/out" || { cat "$work/out"; fail "no explanation"; }
[[ $("$damstack" output greeting) == '"hello"' ]] || fail "applied after a no"

step "an app: added, deployed after the platform and before what comes after the apps"
app=$repo/test/demo-app
"$damstack" stack check "$app" >"$work/out" 2>&1 || { cat "$work/out"; fail "stack check of the app"; }
sed -i.bak 's/^greeting: hi/greeting: hello/' stack.yaml
printf 'hostname: hello.example.org\n' >"$work/app-answers.yaml"
printf 'y\n' | "$damstack" app add --from "$app" --answers "$work/app-answers.yaml" >"$work/out" 2>&1 || { cat "$work/out"; fail "app add"; }
order=$(grep -Eo '^[0-9]+/[0-9]+  (apply|hello/apply|publish)$' "$work/out" | tr '\n' ' ')
[[ $order == "3/5  apply 4/5  hello/apply 5/5  publish " ]] || { cat "$work/out"; fail "steps ran in the order: $order"; }
grep -q 'hostname: hello.example.org' stack.yaml || fail "the settings of the app are not in stack.yaml"
grep -qx 'clients: \[laptop, phone\]' stack.yaml || fail "stack.yaml lost what was there"
grep -q hello.example.org published.txt || fail "the platform did not publish the records of the app"
[[ $("$damstack" hello output message) == '"hello from hello.example.org"' ]] || fail "the command of the app"
[[ $("$damstack" app list | awk 'NR==2{print $1, $2}') == "hello dev" ]] || fail "app list"
printf 'n\n' | "$damstack" app add --from "$app" >"$work/out" 2>&1 && fail "the app was added twice"
grep -q 'already an app' "$work/out" || { cat "$work/out"; fail "no explanation for a second add"; }
deploy </dev/null || { cat "$work/out"; fail "deploy with the app"; }
grep -q 'hello/apply' "$work/out" && grep -q 'Nothing to change' "$work/out" || { cat "$work/out"; fail "a second deploy changed the app"; }

step "status and history"
"$damstack" status >"$work/out"
grep -Eq '^apply +.* ok' "$work/out" && grep -Eq '^hello/apply +.* ok' "$work/out" && grep -Eq '^publish +.* ok' "$work/out" ||
  { cat "$work/out"; fail "status does not show the steps of the platform and the app"; }
"$damstack" history >"$work/out"
[[ $(grep -c ' deploy ' "$work/out") -ge 12 ]] || fail "history is short"
grep -q ' failed ' "$work/out" || { cat "$work/out"; fail "history lost the failed apply"; }

printf '\nall end to end scenarios passed\n'
