package ops

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/eugene-panin/damstack/internal/backup"
	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/kit"
	"github.com/eugene-panin/damstack/internal/library"
	"github.com/eugene-panin/damstack/internal/login"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/secret"
)

// LoadConfig is the config of damstack with the library fetched.
func LoadConfig(ctx context.Context) (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if l, err := library.Default(); err == nil {
		cfg.Library = l.Get(ctx)
	}
	return cfg, nil
}

// MakeKit packs p and its vault password into one encrypted file in to, the
// current directory when empty, and tells the person its passphrase.
func MakeKit(ui UI, p *project.Project, to string) error {
	password, err := p.Password()
	if err != nil {
		return err
	}
	if to == "" {
		if to, err = os.Getwd(); err != nil {
			return err
		}
	}
	now := time.Now()
	path := filepath.Join(to, fmt.Sprintf("%s-kit-%s.age", p.Meta.Name, now.Format("2006-01-02")))
	if _, err := os.Stat(path); err == nil {
		path = filepath.Join(to, fmt.Sprintf("%s-kit-%s.age", p.Meta.Name, now.Format("2006-01-02-150405")))
	}
	tmp, err := os.CreateTemp(to, ".kit-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	passphrase := kit.Passphrase()
	err = kit.Pack(tmp, p.Dir, password, passphrase)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	st, err := backup.LoadState(p.Dir)
	if err != nil {
		return err
	}
	st.Kit = now
	if err := backup.SaveState(p.Dir, st); err != nil {
		return err
	}
	out := ui.Out()
	fmt.Fprintf(out, "The recovery kit of %s is %s.\n\n", p.Meta.Name, path)
	fmt.Fprintf(out, "Its passphrase, shown only now:\n\n    %s\n\n", passphrase)
	fmt.Fprintln(out, "Write the passphrase down, on paper or in a password manager, and keep the file off this")
	fmt.Fprintln(out, "computer: a USB stick, a cloud drive. With both, damstack backup open brings the project back")
	fmt.Fprintln(out, "on any computer; without damstack, age -d opens the file. Make a new kit after every change")
	fmt.Fprintln(out, "to the project: damstack backup status says when one is due.")
	return nil
}

// OpenKit brings a project back on this computer from its recovery kit, into
// into or ~/.damstack/<project>; passphrase asks for the passphrase of the kit.
func OpenKit(ctx context.Context, ui UI, file, into string, passphrase func() (string, error)) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	phrase, err := passphrase()
	if err != nil {
		return err
	}
	cfg, err := LoadConfig(ctx)
	if err != nil {
		return err
	}
	home, err := config.ProjectsDir()
	if err != nil {
		return err
	}
	near := home
	if into != "" {
		abs, err := filepath.Abs(into)
		if err != nil {
			return err
		}
		near = filepath.Dir(abs)
	}
	if err := os.MkdirAll(near, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(near, fmt.Sprintf(".opening-%d", time.Now().UnixNano()))
	password, err := kit.Unpack(f, strings.ToUpper(strings.TrimSpace(phrase)), tmp)
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	p, err := project.Open(tmp)
	if err != nil {
		return fmt.Errorf("the kit holds no project of damstack: %w", err)
	}
	name := p.Meta.Name
	if existing, ok := cfg.Project(name); ok {
		return fmt.Errorf("%s is a project on this computer already, in %s; damstack does not write over it", name, existing.Path)
	}
	dest := into
	if dest == "" {
		dest = filepath.Join(home, name)
	}
	if dest, err = filepath.Abs(dest); err != nil {
		return err
	}
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s is there already; damstack does not write over it", dest)
	}
	passPath, err := project.PasswordPath(name)
	if err != nil {
		return err
	}
	if have, err := os.ReadFile(passPath); err == nil && strings.TrimSpace(string(have)) != password {
		return fmt.Errorf("another vault password of %s is at %s; move it away first", name, passPath)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(passPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(passPath, []byte(password+"\n"), 0o600); err != nil {
		return err
	}
	cfg.Projects = append(cfg.Projects, config.Project{Name: name, Stack: p.Meta.Stack.Name, Path: dest})
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(ui.Out(), "%s is back, in %s, with its vault password in %s.\n", name, dest, passPath)
	fmt.Fprintf(ui.Out(), "damstack status %s shows it; damstack backup pull %s brings its backups to this computer.\n", name, name)
	return nil
}

// KitWarning says when a project has no recovery kit, or changed since its
// last one.
func KitWarning(p *project.Project, st backup.State) string {
	switch {
	case st.Kit.IsZero():
		return fmt.Sprintf("%s has no recovery kit: without this computer, nothing brings the project back. damstack backup kit %s makes one.", p.Meta.Name, p.Meta.Name)
	case kit.Changed(p.Dir).After(st.Kit):
		return fmt.Sprintf("%s changed since its recovery kit of %s: damstack backup kit %s makes a new one; keep it instead of the old.",
			p.Meta.Name, st.Kit.Local().Format("2006-01-02"), p.Meta.Name)
	}
	return ""
}

// Backup is the backup of a project, rendered, with the stack it is of.
type Backup struct {
	m    *manifest.Manifest
	plan *backup.Plan
	// own opens the copy of the backups on this machine; the server's
	// password until a pull gives it one of its own.
	own string
}

// LoadBackup is the backup of p, as its stack makes it.
func LoadBackup(ctx context.Context, p *project.Project) (*Backup, error) {
	m, _ := CachedStack(p)
	if m == nil {
		var err error
		if m, _, err = ProjectStack(ctx, p, ""); err != nil {
			return nil, err
		}
	}
	if m.Backup == nil {
		return nil, fmt.Errorf("the stack %s of %s makes no backups for damstack to pull", m.Name, p.Meta.Name)
	}
	cfg, err := p.Config()
	if err != nil {
		return nil, err
	}
	password, err := p.Password()
	if err != nil {
		return nil, err
	}
	secrets, err := p.Secrets(password)
	if err != nil {
		return nil, err
	}
	plan, err := backup.Resolve(m.Backup, cfg, secrets, p.Dir)
	if err != nil {
		return nil, err
	}
	own, _ := secrets[backup.CopySecret].(string)
	if own == "" {
		own = plan.Password
	}
	return &Backup{m: m, plan: plan, own: own}, nil
}

// ownCopy gives the copy of the backups of p a password of its own, kept in
// vault.yml, when it has none yet; the next pull puts it in place.
func (b *Backup) ownCopy(p *project.Project) error {
	if b.own != b.plan.Password {
		return nil
	}
	local, err := LockHere(p)
	if err != nil {
		return err
	}
	defer local.Close()
	password, err := p.Password()
	if err != nil {
		return err
	}
	secrets, err := p.Secrets(password)
	if err != nil {
		return err
	}
	if v, _ := secrets[backup.CopySecret].(string); v != "" {
		b.own = v
		return nil
	}
	g, err := secret.Generate("password", 0, "")
	if err != nil {
		return err
	}
	secrets[backup.CopySecret] = g.Value
	if err := p.SaveSecrets(password, secrets); err != nil {
		return err
	}
	b.own = g.Value
	return nil
}

func (b *Backup) repo(ctx context.Context, ui UI) (*backup.Repo, error) {
	tb, _, err := FetchToolbox(ctx, ui.Err(), nil)
	if err != nil {
		return nil, err
	}
	return &backup.Repo{Restic: filepath.Join(tb, "bin", "restic"), Path: b.plan.To, Password: b.own, FromPassword: b.plan.Password,
		Out: ui.Out(), Err: ui.Err()}, nil
}

// PullBackup copies the new backups of p from the server to this machine,
// then drops the old ones as stack.yaml says; scheduled, it skips quietly
// when the server does not answer.
func PullBackup(ctx context.Context, ui UI, p *project.Project, scheduled bool) error {
	b, err := LoadBackup(ctx, p)
	if err != nil {
		return err
	}
	stamp := func() string {
		if scheduled {
			return time.Now().Format(time.DateTime) + " "
		}
		return ""
	}
	var from backup.Source
	if srv := b.m.Server; srv != nil {
		if from, err = serverSource(ctx, ui, p, srv, b.plan.From, scheduled); err != nil {
			if errors.Is(err, errNoAnswer) {
				fmt.Fprintf(ui.Out(), "%s%v\n", stamp(), err)
				return nil
			}
			return err
		}
	} else {
		from = backup.Source{Repo: filepath.Join(p.Dir, b.plan.From)}
		if _, err := os.Stat(filepath.Join(from.Repo, "config")); err != nil {
			return fmt.Errorf("there are no backups at %s yet", from.Repo)
		}
	}
	if err := b.ownCopy(p); err != nil {
		return err
	}
	repo, err := b.repo(ctx, ui)
	if err != nil {
		return err
	}
	st, err := backup.LoadState(p.Dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(ui.Out(), "%sPulling the backups of %s to %s.\n", stamp(), p.Meta.Name, b.plan.To)
	start := time.Now()
	_, later, err := repo.Copy(ctx, from, st.Latest, start)
	if err == nil {
		err = repo.Forget(ctx, b.plan.Keep)
	}
	var snaps []backup.Snapshot
	if err == nil {
		snaps, err = repo.Snapshots(ctx)
	}
	if rerr := Record(p, "backup pull", "", start, err); rerr != nil && err == nil {
		err = rerr
	}
	if err != nil {
		return fmt.Errorf("the pull failed: %w; nothing on this machine was lost", err)
	}
	st.Pulled = start
	st.Saw(snaps)
	if err := backup.SaveState(p.Dir, st); err != nil {
		return err
	}
	if len(later) > 0 {
		fmt.Fprintf(ui.Err(), "%s%s\n", stamp(), laterThanNow(later))
	}
	fmt.Fprintf(ui.Out(), "%s%s\n", stamp(), describeState(st))
	return nil
}

// laterThanNow says which backups on the server were not copied, dated
// later than now.
func laterThanNow(snaps []backup.Snapshot) string {
	var lines []string
	for _, s := range snaps {
		lines = append(lines, "  "+s.ID+"  "+s.Time.Local().Format(time.DateTime))
	}
	return "These backups on the server are dated later than now, and were not copied:\n" + strings.Join(lines, "\n") +
		"\nThe clock of the server may be wrong, or someone made them there to have the good ones dropped: look at the server."
}

var errNoAnswer = errors.New("the server does not answer")

// serverSource is the repository at path on the server of p, through the
// tunnel, or at its public address when another tunnel or none is up; in a
// terminal it waits for the tunnel when neither answers, scheduled it does not.
func serverSource(ctx context.Context, ui UI, p *project.Project, srv *manifest.Server, path string, scheduled bool) (backup.Source, error) {
	cfg, err := p.Config()
	if err != nil {
		return backup.Source{}, err
	}
	address, err := manifest.Render("server.tunnel", srv.Tunnel, cfg, nil)
	if err != nil {
		return backup.Source{}, fmt.Errorf("server.tunnel of the stack: %w", err)
	}
	public, err := manifest.RenderLogin("server.address", srv.Address, cfg, nil)
	if err != nil {
		return backup.Source{}, fmt.Errorf("server.address of the stack: %w", err)
	}
	user, err := manifest.RenderLogin("server.ops_user", srv.OpsUser, cfg, nil)
	if err != nil {
		return backup.Source{}, fmt.Errorf("server.ops_user of the stack: %w", err)
	}
	if address == "" || user == "" {
		return backup.Source{}, errors.New("the stack names no tunnel address or ops user of the server to pull the backups from")
	}
	knownHosts := filepath.Join(p.Dir, project.KnownHosts)
	host, err := login.Reach(ctx, knownHosts, public, net.JoinHostPort(address, "22"), net.JoinHostPort(public, "22"))
	switch {
	case err == nil:
		address, _, _ = net.SplitHostPort(host)
	case scheduled:
		if errors.Is(err, login.ErrOtherServer) {
			return backup.Source{}, fmt.Errorf("%w at %s or %s: another server answers there; the next pull tries again", errNoAnswer, address, public)
		}
		return backup.Source{}, fmt.Errorf("%w at %s or %s; the next pull tries again", errNoAnswer, address, public)
	default:
		if err := WaitTunnel(ctx, ui, p, srv); err != nil {
			return backup.Source{}, err
		}
	}
	m, _ := CachedStack(p)
	password, err := p.Password()
	if err != nil {
		return backup.Source{}, err
	}
	key, err := KeyFor(p, m, password)
	if err != nil {
		return backup.Source{}, err
	}
	return backup.SFTP(user, address, path, knownHosts, key), nil
}

func describeState(st backup.State) string {
	if st.Snapshots == 0 {
		return "There are no snapshots on this machine yet."
	}
	text := fmt.Sprintf("%d snapshots on this machine, the latest from %s (%s).", st.Snapshots,
		st.Latest.Local().Format("2006-01-02 15:04"), Ago(st.Latest))
	return text
}

// BackupStatus shows the backups of p on this machine and when they were
// last pulled.
func BackupStatus(ctx context.Context, ui UI, p *project.Project) error {
	b, err := LoadBackup(ctx, p)
	if err != nil {
		return err
	}
	out := ui.Out()
	fmt.Fprintf(out, "The backups of %s: the server makes them, this machine keeps a copy at %s.\n", p.Meta.Name, b.plan.To)
	st, err := backup.LoadState(p.Dir)
	if err != nil {
		return err
	}
	repo, err := b.repo(ctx, ui)
	if err != nil {
		return err
	}
	if repo.Exists() {
		snaps, err := repo.Snapshots(ctx)
		if err != nil {
			return err
		}
		st.Saw(snaps)
		if err := backup.SaveState(p.Dir, st); err != nil {
			return err
		}
	} else {
		st.Saw(nil)
	}
	fmt.Fprintln(out, describeState(st))
	if entries, err := p.History(); err == nil {
		for i := len(entries) - 1; i >= 0; i-- {
			if e := entries[i]; e.Command == "backup pull" {
				fmt.Fprintf(out, "The last pull: %s, %s.\n", e.Time.Local().Format("2006-01-02 15:04"), e.Result)
				if e.Error != "" {
					fmt.Fprintf(out, "  %s\n", FirstLine(e.Error))
				}
				break
			}
		}
	}
	switch {
	case b.plan.Every == 0:
		fmt.Fprintf(out, "Pulls run only by hand: damstack backup pull %s. stack.yaml says how often they run by themselves.\n", p.Meta.Name)
	case backup.Scheduled(ctx, p.Meta.Name):
		fmt.Fprintf(out, "Pulls run by themselves every %s while this machine is on; what they do is in %s.\n", backup.FormatEvery(b.plan.Every), backup.Where(p.Meta.Name))
	default:
		fmt.Fprintf(out, "Pulls should run every %s but are not scheduled: damstack backup schedule %s.\n", backup.FormatEvery(b.plan.Every), p.Meta.Name)
	}
	for _, warn := range []string{StaleWarning(st), KitWarning(p, st)} {
		if warn != "" {
			fmt.Fprintln(out)
			fmt.Fprintln(out, warn)
		}
	}
	return nil
}

// StaleWarning says what to do when the latest snapshot on this machine is
// old, or there is none.
func StaleWarning(st backup.State) string {
	switch {
	case st.Snapshots == 0:
		return "Nothing is backed up on this machine yet: if the server is lost, so is its data. damstack backup pull copies the backups here."
	case time.Since(st.Latest) > backup.Stale:
		return fmt.Sprintf("The latest backup on this machine is from %s: either the server stopped backing up, or the pulls fail. damstack backup pull says which.", Ago(st.Latest))
	}
	return ""
}

// ScheduleBackup makes the pulls of a project run as stack.yaml says; loud
// says so even when nothing changed.
func ScheduleBackup(ctx context.Context, ui UI, p *project.Project, b *Backup, loud bool) error {
	self, err := selfPath()
	if err != nil {
		return err
	}
	env := map[string]string{"PATH": "/usr/bin:/bin:/usr/sbin:/sbin"}
	for _, name := range []string{"DAMSTACK_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME"} {
		if v := os.Getenv(name); v != "" {
			env[name] = v
		}
	}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" && !strings.Contains(sock, "com.apple.launchd") {
		env["SSH_AUTH_SOCK"] = sock
	}
	j := backup.Job{Project: p.Meta.Name, Args: []string{self, "backup", "pull", p.Meta.Name, "--scheduled"}, Env: env, Every: b.plan.Every}
	changed, err := backup.Install(ctx, j)
	if err != nil {
		return err
	}
	switch {
	case b.plan.Every == 0 && (changed || loud):
		fmt.Fprintf(ui.Out(), "Backups of %s are pulled only by hand: damstack backup pull %s.\n", p.Meta.Name, p.Meta.Name)
	case b.plan.Every > 0 && (changed || loud):
		fmt.Fprintf(ui.Out(), "Backups of %s are pulled to %s every %s while this machine is on; damstack backup status shows them.\n",
			p.Meta.Name, b.plan.To, backup.FormatEvery(b.plan.Every))
	}
	return nil
}

// selfPath is the damstack a schedule runs: the one on the PATH when it is
// this one, which outlives upgrades, else this executable.
func selfPath() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if found, err := exec.LookPath("damstack"); err == nil {
		a, _ := filepath.EvalSymlinks(found)
		b, _ := filepath.EvalSymlinks(self)
		if a != "" && a == b {
			return filepath.Abs(found)
		}
	}
	return self, nil
}
