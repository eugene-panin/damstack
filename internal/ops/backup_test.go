package ops

import (
	"strings"
	"testing"
	"time"

	"github.com/eugene-panin/damstack/internal/backup"
)

func TestBackupsDatedLaterThanNowAreNamed(t *testing.T) {
	at := time.Date(2031, 1, 2, 3, 4, 5, 0, time.Local)
	text := laterThanNow([]backup.Snapshot{{ID: "4f2a9c1e", Time: at}, {ID: "b7d03e55", Time: at.Add(time.Hour)}})
	for _, want := range []string{
		"These backups on the server are dated later than now, and were not copied:\n",
		"  4f2a9c1e  2031-01-02 03:04:05\n  b7d03e55  2031-01-02 04:04:05\n",
		"The clock of the server may be wrong, or someone made them there to have the good ones dropped: look at the server.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
}

func TestTheStateOfTheBackupsHereIsSaid(t *testing.T) {
	recent := time.Now().Add(-2 * time.Hour)
	old := time.Now().Add(-backup.Stale - time.Hour)
	for _, c := range []struct {
		name         string
		st           backup.State
		state, stale string
	}{
		{"none", backup.State{}, "There are no snapshots on this machine yet.", "Nothing is backed up on this machine yet"},
		{"recent", backup.State{Snapshots: 3, Latest: recent}, "3 snapshots on this machine, the latest from " + recent.Local().Format("2006-01-02 15:04") + " (2 h ago).", ""},
		{"old", backup.State{Snapshots: 1, Latest: old}, "1 snapshots on this machine", "either the server stopped backing up, or the pulls fail"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := describeState(c.st); !strings.HasPrefix(got, c.state) {
				t.Errorf("state: %q", got)
			}
			got := StaleWarning(c.st)
			if c.stale == "" && got != "" || !strings.Contains(got, c.stale) {
				t.Errorf("warning: %q", got)
			}
		})
	}
}

func TestTheCopyOfTheBackupsGetsAPasswordOfItsOwnOnce(t *testing.T) {
	p, password := newProject(t, "server: {address: 203.0.113.10}\n")
	if err := p.SaveSecrets(password, map[string]any{"restic_password": "the-servers"}); err != nil {
		t.Fatal(err)
	}
	fresh := func() *Backup { return &Backup{plan: &backup.Plan{Password: "the-servers"}, own: "the-servers"} }

	here, err := LockHere(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh().ownCopy(p); err == nil || !strings.Contains(err.Error(), "another damstack is changing demo") {
		t.Fatalf("the password is made under the lock here: %v", err)
	}
	here.Close()

	b := fresh()
	if err := b.ownCopy(p); err != nil {
		t.Fatal(err)
	}
	secrets, _ := p.Secrets(password)
	made, _ := secrets[backup.CopySecret].(string)
	if made == "" || made == "the-servers" || b.own != made || secrets["restic_password"] != "the-servers" {
		t.Fatalf("own %q, vault %v", b.own, secrets)
	}

	again := fresh()
	if err := again.ownCopy(p); err != nil {
		t.Fatal(err)
	}
	secrets, _ = p.Secrets(password)
	if again.own != made || secrets[backup.CopySecret] != made {
		t.Errorf("a second call keeps the password: %q, %v", again.own, secrets[backup.CopySecret])
	}

	here, err = LockHere(p)
	if err != nil {
		t.Fatal(err)
	}
	defer here.Close()
	if err := (&Backup{plan: &backup.Plan{Password: "the-servers"}, own: made}).ownCopy(p); err != nil {
		t.Errorf("a copy with its own password has nothing to do, and needs no lock: %v", err)
	}
}
