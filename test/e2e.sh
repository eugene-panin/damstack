#!/usr/bin/env bash
# damstack end to end with the demo stack in test/demo and the real toolbox:
# set up, deploy, deploy again from a poisoned shell, a policy that denies, no
# apply without --yes, apps, backups and the recovery kit. With no terminal,
# answers come from --answers and --yes goes ahead, as in any script.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$HOME/.cache"
work=$(mktemp -d "$HOME/.cache/damstack-e2e.XXXXXX")
trap 'rm -rf "$work"' EXIT
export XDG_CONFIG_HOME=$work/config XDG_CACHE_HOME=$work/cache

damstack=$work/damstack
(cd "$repo" && go build -o "$damstack" ./cmd/damstack)
toolbox=$("$damstack" version | sed -n 's/.*toolbox //p')
shared=$HOME/.cache/damstack/toolbox
mkdir -p "$shared" "$XDG_CACHE_HOME/damstack"
ln -s "$shared" "$XDG_CACHE_HOME/damstack/toolbox"
stack=$repo/test/demo
project=$work/demo1

step() { printf '\n== %s\n' "$*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
deploy() { "$damstack" deploy --verbose "$@" >"$work/out" 2>&1; }

step "stack check proves the demo stack"
"$damstack" stack check "$stack" >"$work/out" 2>&1 || { cat "$work/out"; fail "stack check"; }

step "a new project: questions, secrets, and every step"
cd "$work"
printf 'clients: [laptop, phone]\nextra: false\napi_token: tok\n' >"$work/answers.yaml"
deploy --from "$stack" --name demo1 --dir "$project" --answers "$work/answers.yaml" --yes </dev/null || { cat "$work/out"; fail "first deploy"; }
cd "$project"
[[ $(cat greeting.txt) == "hello, laptop and phone" ]] || fail "the playbook did not write the greeting"
head -1 vault.yml | grep -q '^\$ANSIBLE_VAULT;1.1;AES256' || fail "vault.yml is not encrypted"
grep -q 'root_token\|tok' vault.yml && fail "a secret is in vault.yml in the clear"
[[ -e .damstack/work/init.json ]] && fail "the kept file was left"
grep -q 'BEGIN CERTIFICATE' ca.pem || fail "no CA certificate"
grep -q '^Done. demo1 is running.' "$work/out" && grep -qx '  greeting: hello' "$work/out" || { cat "$work/out"; fail "no ending"; }
[[ $("$damstack" token api --print) == tok ]] || fail "damstack token"
[[ $(stat -f %Lp "$XDG_CONFIG_HOME/damstack/projects/demo1/vault-pass" 2>/dev/null || stat -c %a "$XDG_CONFIG_HOME/damstack/projects/demo1/vault-pass") == 600 ]] ||
  fail "the vault password is readable by others"
[[ $("$damstack" output greeting) == '"hello"' ]] || fail "the output command"

step "a second deploy from a poisoned shell changes nothing"
poison=$work/poison
mkdir -p "$poison/bin" "$poison/py/ansible"
for cmd in tofu conftest ansible-playbook ansible-galaxy python3 restic; do
  printf '#!/bin/sh\necho "the %s of the machine ran" >&2\nexit 99\n' "$cmd" >"$poison/bin/$cmd"
  chmod 0755 "$poison/bin/$cmd"
done
echo 'raise SystemExit("an ansible of the machine was imported")' >"$poison/py/ansible/__init__.py"
printf 'provider_installation {\n  network_mirror {\n    url = "https://127.0.0.1:9/"\n  }\n}\n' >"$poison/tofurc"
env PATH="$poison/bin:$PATH" PYTHONPATH="$poison/py" PYTHONHOME="$poison" TF_VAR_greeting=poisoned \
  ANSIBLE_CONFIG=/nonexistent/ansible.cfg ANSIBLE_STDOUT_CALLBACK=no_such_callback TF_CLI_CONFIG_FILE="$poison/tofurc" \
  "$damstack" deploy --verbose </dev/null >"$work/out" 2>&1 || { cat "$work/out"; fail "second deploy"; }
grep -q 'of the machine ran\|an ansible of the machine' "$work/out" && { cat "$work/out"; fail "a tool of the machine ran"; }
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

step "without a terminal, a change is not applied unless --yes says so"
sed -i.bak 's/^greeting: bye/greeting: hi/' stack.yaml
"$damstack" deploy </dev/null >"$work/out" 2>&1 && fail "a change went through without --yes"
grep -q '1 to change. Go?' "$work/out" || { cat "$work/out"; fail "the question does not count the changes"; }
grep -q 'pass --yes' "$work/out" || { cat "$work/out"; fail "no explanation"; }
[[ $("$damstack" output greeting) == '"hello"' ]] || fail "applied without --yes"

step "an app: added, deployed after the platform and before what comes after the apps"
app=$repo/test/demo-app
"$damstack" stack check "$app" >"$work/out" 2>&1 || { cat "$work/out"; fail "stack check of the app"; }
sed -i.bak 's/^greeting: hi/greeting: hello/' stack.yaml
printf 'hostname: hello.example.org\n' >"$work/app-answers.yaml"
"$damstack" app add --from "$app" --answers "$work/app-answers.yaml" --yes </dev/null >"$work/out" 2>&1 || { cat "$work/out"; fail "app add"; }
order=$(grep -Eo '^[0-9]+/[0-9]+  (apply|hello/apply|publish)$' "$work/out" | tr '\n' ' ')
[[ $order == "3/5  apply 4/5  hello/apply 5/5  publish " ]] || { cat "$work/out"; fail "steps ran in the order: $order"; }
grep -q 'hostname: hello.example.org' stack.yaml || fail "the settings of the app are not in stack.yaml"
grep -qx 'clients: \[laptop, phone\]' stack.yaml || fail "stack.yaml lost what was there"
grep -q hello.example.org published.txt || fail "the platform did not publish the records of the app"
grep -qx '  hello at hello.example.org' "$work/out" || { cat "$work/out"; fail "no ending of the app"; }
[[ $("$damstack" hello output message) == '"hello from hello.example.org"' ]] || fail "the command of the app"
[[ $("$damstack" app list | awk 'NR==2{print $1, $2}') == "hello dev" ]] || fail "app list"
"$damstack" app add --from "$app" </dev/null >"$work/out" 2>&1 && fail "the app was added twice"
grep -q 'already an app' "$work/out" || { cat "$work/out"; fail "no explanation for a second add"; }
deploy </dev/null || { cat "$work/out"; fail "deploy with the app"; }
grep -q 'hello/apply' "$work/out" && grep -q 'Nothing to change' "$work/out" || { cat "$work/out"; fail "a second deploy changed the app"; }

step "from anywhere: a project by its name, with -p, the only one, and the current one"
cd "$work"
grep -q '^demo1, in ' <<<"$("$damstack" status demo1)" || fail "status by name"
[[ $("$damstack" -p demo1 output greeting) == '"hello"' ]] || fail "-p before the command"
[[ $("$damstack" output greeting -p demo1) == '"hello"' ]] || fail "-p after the command"
grep -q '^demo1, in ' <<<"$("$damstack" status)" || fail "the only project"
grep -q 'demo1 is the current project' <<<"$("$damstack" use demo1)" || fail "use"
grep -q '^\* demo1' <<<"$("$damstack")" || fail "the home screen does not mark the current project"
[[ $("$damstack" hello output message) == '"hello from hello.example.org"' ]] || fail "a command of an app from anywhere"
deploy demo1 </dev/null || { cat "$work/out"; fail "deploy by the name of the project"; }
grep -q 'Done. demo1 is running.' "$work/out" || { cat "$work/out"; fail "deploy demo1 did not deploy it"; }
printf '' | "$damstack" deploy --from "$stack" --name hashi --dir "$work/other" >"$work/out" 2>&1 && fail "a project took the name of a stack"
grep -q "hashi cannot be the name" "$work/out" || { cat "$work/out"; fail "no reason for the refused name"; }
cd "$project"

step "status --live checks the server and changes nothing"
"$damstack" status --live >"$work/out" 2>&1 || { cat "$work/out"; fail "status --live"; }
grep -Eq '^apply +as stack.yaml says' "$work/out" && grep -Eq '^configure +as stack.yaml says' "$work/out" &&
  grep -Eq '^init +runs once' "$work/out" && grep -q 'The server is as stack.yaml says' "$work/out" || { cat "$work/out"; fail "a clean check"; }
sed -i.bak 's/^greeting: hello/greeting: hi/' stack.yaml
"$damstack" status --live >"$work/out" 2>&1
grep -Eq '^apply +differs: 1 to change' "$work/out" && grep -q 'step(s) differ from stack.yaml' "$work/out" || { cat "$work/out"; fail "the drift is not seen"; }
[[ $("$damstack" output greeting) == '"hello"' ]] || fail "status --live changed something"
mv stack.yaml.bak stack.yaml

step "backups: pulled from the server, the old ones dropped, the state shown"
"$damstack" restore --yes >"$work/out" 2>&1 && fail "a restore with no backups went through"
grep -q 'no backups of demo1 are on this machine' "$work/out" || { cat "$work/out"; fail "no reason for a restore with no backups"; }
"$damstack" backup pull >"$work/out" 2>&1 && fail "a pull with no backups went through"
grep -q 'no backups at .*server-backups yet' "$work/out" || { cat "$work/out"; fail "no reason for a pull with no backups"; }
grep -q 'Nothing is backed up on this machine yet' <<<"$("$damstack")" || fail "the home screen does not warn of no backups"
restic=$XDG_CACHE_HOME/damstack/toolbox/$toolbox/bin/restic
"$damstack" backup >/dev/null 2>&1 || true
[[ -x $restic ]] || fail "restic was not fetched"
export RESTIC_PASSWORD=tok
"$restic" -r server-backups init -q
for i in 1 2 3 4 5; do echo "$i" >backed-up.txt; "$restic" -r server-backups backup -q --tag scheduled backed-up.txt; done
unset RESTIC_PASSWORD
"$damstack" backup pull >"$work/out" 2>&1 || { cat "$work/out"; fail "backup pull"; }
grep -q '^3 snapshots on this machine' "$work/out" || { cat "$work/out"; fail "the pull did not keep the last 3"; }
"$damstack" backup pull >"$work/out" 2>&1 || { cat "$work/out"; fail "a second pull"; }
grep -q '^3 snapshots on this machine' "$work/out" || { cat "$work/out"; fail "a second pull changed the count"; }
"$damstack" backup status >"$work/out" 2>&1 || { cat "$work/out"; fail "backup status"; }
grep -q '^3 snapshots on this machine, the latest from' "$work/out" && grep -q '^The last pull: .*, ok' "$work/out" &&
  grep -q 'Pulls run only by hand' "$work/out" || { cat "$work/out"; fail "backup status says"; }
grep -q 'Nothing is backed up' <<<"$("$damstack")" && fail "the home screen still warns"
rm -f backed-up.txt

step "restore: every step again, once ones too, and the restore steps with the latest snapshot"
"$damstack" restore >"$work/out" 2>&1 </dev/null && fail "a restore went ahead without --yes"
"$damstack" restore --yes >"$work/out" 2>&1 </dev/null || { cat "$work/out"; fail "restore"; }
grep -q 'demo1 is back from the backup of' "$work/out" || { cat "$work/out"; fail "restore says"; }
[[ $(cat restored.txt) == 5 ]] || fail "the restore step did not get the latest snapshot"
[[ -e .damstack/work/restore ]] && fail "the snapshot was left on this machine"
"$damstack" history >"$work/out"
grep -q ' restore .*restore-data' "$work/out" || { cat "$work/out"; fail "the history has no restore"; }
grep -q ' restore .*init' "$work/out" || { cat "$work/out"; fail "the once step did not run again"; }
rm -f restored.txt
"$damstack" deploy --verbose --yes >"$work/out" 2>&1 </dev/null || { cat "$work/out"; fail "a deploy after the restore"; }
grep -q 'restored the data' "$work/out" && fail "a deploy ran the restore step"
[[ -e restored.txt ]] && fail "a deploy restored"
grep -q 'init: done before' "$work/out" || fail "after the restore, the once step runs again"

step "a recovery kit brings the project back on another computer"
grep -q 'demo1 has no recovery kit' <<<"$("$damstack")" || fail "the home screen does not ask for a kit"
"$damstack" backup kit --to "$work" >"$work/out" 2>&1 || { cat "$work/out"; fail "backup kit"; }
kit=$(ls "$work"/demo1-kit-*.age)
passphrase=$(grep -Eo '^    [0-9A-Z]{4}(-[0-9A-Z]{4}){4}$' "$work/out" | tr -d ' ')
[[ -n $passphrase ]] || { cat "$work/out"; fail "no passphrase shown"; }
grep -q 'tok' "$kit" && fail "the kit is not encrypted"
grep -q 'recovery kit' <<<"$("$damstack" backup status)" && fail "a kit is asked for right after one was made"
touch stack.yaml
grep -q 'demo1 changed since its recovery kit' <<<"$("$damstack" backup status)" || fail "a change to the project does not ask for a new kit"
other=(env XDG_CONFIG_HOME="$work/config2" DAMSTACK_HOME="$work/home2")
"${other[@]}" "$damstack" backup open "$kit" <<<"AAAA-AAAA-AAAA-AAAA-AAAA" >"$work/out" 2>&1 && fail "a wrong passphrase opened the kit"
grep -q 'does not open this kit' "$work/out" || { cat "$work/out"; fail "no reason for a wrong passphrase"; }
[[ -e $work/home2/demo1 ]] && fail "a wrong passphrase left the project"
"${other[@]}" "$damstack" backup open "$kit" <<<"$passphrase" >"$work/out" 2>&1 || { cat "$work/out"; fail "backup open"; }
grep -q "demo1 is back, in $work/home2/demo1" "$work/out" || { cat "$work/out"; fail "backup open says"; }
[[ $("${other[@]}" "$damstack" token api --print -p demo1) == tok ]] || fail "the vault password did not come back"
[[ $(cd "$work" && "${other[@]}" "$damstack" output greeting -p demo1) == '"hello"' ]] || fail "the OpenTofu state did not come back"
[[ -e $work/home2/demo1/.damstack/work/logs ]] && fail "the work directory went into the kit"
"${other[@]}" "$damstack" backup open "$kit" <<<"$passphrase" >"$work/out" 2>&1 && fail "a kit opened over a project that is there"
grep -q 'is a project on this computer already' "$work/out" || { cat "$work/out"; fail "no reason for a second open"; }

step "status and history"
"$damstack" status >"$work/out"
grep -Eq '^apply +.* ok' "$work/out" && grep -Eq '^hello/apply +.* ok' "$work/out" && grep -Eq '^publish +.* ok' "$work/out" ||
  { cat "$work/out"; fail "status does not show the steps of the platform and the app"; }
"$damstack" history >"$work/out"
[[ $(grep -c ' deploy ' "$work/out") -ge 12 ]] || fail "history is short"
grep -q ' failed ' "$work/out" || { cat "$work/out"; fail "history lost the failed apply"; }

step "rename: the project answers to its new name, with its password, state and history"
"$damstack" rename demo1 hashi >"$work/out" 2>&1 && fail "a project took the name of a stack"
"$damstack" rename demo1 demo3 >"$work/out" 2>&1 || { cat "$work/out"; fail "rename"; }
grep -q "demo1 is now demo3, in $project" "$work/out" || { cat "$work/out"; fail "rename says"; }
[[ $("$damstack" token api --print -p demo3) == tok ]] || fail "the vault password did not follow the name"
[[ -e $XDG_CONFIG_HOME/damstack/projects/demo1 ]] && fail "the old vault password directory was left"
"$damstack" status -p demo1 >"$work/out" 2>&1 && fail "the old name still works"
"$damstack" history -p demo3 >"$work/out"
grep -q ' deploy ' "$work/out" || { cat "$work/out"; fail "the history did not follow the name"; }
"${other[@]}" "$damstack" rename demo1 demo4 >"$work/out" 2>&1 || { cat "$work/out"; fail "rename in the default place"; }
[[ -e $work/home2/demo1 ]] && fail "the directory in the default place kept the old name"
[[ $(cd "$work" && "${other[@]}" "$damstack" output greeting -p demo4) == '"hello"' ]] || fail "the OpenTofu state did not follow the directory"

printf '\nall end to end scenarios passed\n'
