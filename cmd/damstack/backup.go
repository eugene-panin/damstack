package main

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

	"github.com/spf13/cobra"

	"github.com/eugene-panin/damstack/internal/backup"
	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/kit"
	"github.com/eugene-panin/damstack/internal/login"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
)

func backupCommand(s *streams) *cobra.Command {
	status := func(cmd *cobra.Command, args []string) error {
		p, err := pickProject(s, firstArg(args))
		if err != nil {
			return err
		}
		return backupStatus(cmd.Context(), s, p)
	}
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "The backups of a project: the server makes them every night, this machine pulls them",
		Args:  cobra.NoArgs,
		RunE:  status,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "status [project]",
		Short: "Show the backups on this machine and when they were last pulled",
		Args:  cobra.MaximumNArgs(1),
		RunE:  status,
	})
	var scheduled bool
	pull := &cobra.Command{
		Use:   "pull [project]",
		Short: "Copy the new backups from the server to this machine, then drop the old ones as stack.yaml says",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := pickProject(s, firstArg(args))
			if err != nil {
				return err
			}
			return backupPull(cmd.Context(), s, p, scheduled)
		},
	}
	pull.Flags().BoolVar(&scheduled, "scheduled", false, "run as the scheduled pull: skip quietly when the server does not answer")
	pull.Flags().MarkHidden("scheduled")
	cmd.AddCommand(pull)
	cmd.AddCommand(&cobra.Command{
		Use:   "schedule [project]",
		Short: "Pull the backups by themselves, as often as stack.yaml says",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := pickProject(s, firstArg(args))
			if err != nil {
				return err
			}
			b, err := loadBackup(cmd.Context(), p)
			if err != nil {
				return err
			}
			return scheduleBackup(cmd.Context(), s, p, b, true)
		},
	})
	var to string
	kitCmd := &cobra.Command{
		Use:   "kit [project]",
		Short: "Pack the project and its vault password into one encrypted file, to bring it back on another computer",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := pickProject(s, firstArg(args))
			if err != nil {
				return err
			}
			return makeKit(s, p, to)
		},
	}
	kitCmd.Flags().StringVar(&to, "to", "", "the directory to write the kit to, the current one by default")
	cmd.AddCommand(kitCmd)
	var into string
	open := &cobra.Command{
		Use:   "open <kit>",
		Short: "Bring a project back on this computer from its recovery kit",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return openKit(cmd.Context(), s, args[0], into)
		},
	}
	open.Flags().StringVar(&into, "dir", "", "where the project goes, ~/.damstack/<project> by default")
	cmd.AddCommand(open)
	return cmd
}

func makeKit(s *streams, p *project.Project, to string) error {
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
	fmt.Fprintf(s.out, "The recovery kit of %s is %s.\n\n", p.Meta.Name, path)
	fmt.Fprintf(s.out, "Its passphrase, shown only now:\n\n    %s\n\n", passphrase)
	fmt.Fprintln(s.out, "Write the passphrase down, on paper or in a password manager, and keep the file off this")
	fmt.Fprintln(s.out, "computer: a USB stick, a cloud drive. With both, damstack backup open brings the project back")
	fmt.Fprintln(s.out, "on any computer; without damstack, age -d opens the file. Make a new kit after every change")
	fmt.Fprintln(s.out, "to the project: damstack backup status says when one is due.")
	return nil
}

func openKit(ctx context.Context, s *streams, file, into string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	passphrase, err := s.prompt.Secret("The passphrase of the kit")
	if err != nil {
		return err
	}
	cfg, err := loadConfig(ctx)
	if err != nil {
		return err
	}
	home, err := config.ProjectsDir()
	if err != nil {
		return err
	}
	tmp := filepath.Join(home, fmt.Sprintf(".opening-%d", time.Now().UnixNano()))
	password, err := kit.Unpack(f, strings.ToUpper(strings.TrimSpace(passphrase)), tmp)
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
	fmt.Fprintf(s.out, "%s is back, in %s, with its vault password in %s.\n", name, dest, passPath)
	fmt.Fprintf(s.out, "damstack status %s shows it; damstack backup pull %s brings its backups to this computer.\n", name, name)
	return nil
}

// kitWarning says when a project has no recovery kit, or changed since its
// last one.
func kitWarning(p *project.Project, st backup.State) string {
	switch {
	case st.Kit.IsZero():
		return fmt.Sprintf("%s has no recovery kit: without this computer, nothing brings the project back. damstack backup kit %s makes one.", p.Meta.Name, p.Meta.Name)
	case kit.Changed(p.Dir).After(st.Kit):
		return fmt.Sprintf("%s changed since its recovery kit of %s: damstack backup kit %s makes a new one; keep it instead of the old.",
			p.Meta.Name, st.Kit.Local().Format("2006-01-02"), p.Meta.Name)
	}
	return ""
}

// projectBackup is the backup of a project, rendered, with the stack it is of.
type projectBackup struct {
	m    *manifest.Manifest
	plan *backup.Plan
}

func loadBackup(ctx context.Context, p *project.Project) (*projectBackup, error) {
	m, _ := cachedStack(p)
	if m == nil {
		var err error
		if m, _, err = projectStack(ctx, p, ""); err != nil {
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
	return &projectBackup{m: m, plan: plan}, nil
}

func (b *projectBackup) repo(ctx context.Context, s *streams) (*backup.Repo, error) {
	tb, _, err := fetchToolbox(ctx, s, nil)
	if err != nil {
		return nil, err
	}
	return &backup.Repo{Restic: filepath.Join(tb, "bin", "restic"), Path: b.plan.To, Password: b.plan.Password, Out: s.out, Err: s.err}, nil
}

func backupPull(ctx context.Context, s *streams, p *project.Project, scheduled bool) error {
	b, err := loadBackup(ctx, p)
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
		if from, err = serverSource(ctx, s, p, srv, b.plan.From, scheduled); err != nil {
			if errors.Is(err, errNoAnswer) {
				fmt.Fprintf(s.out, "%s%v\n", stamp(), err)
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
	repo, err := b.repo(ctx, s)
	if err != nil {
		return err
	}
	fmt.Fprintf(s.out, "%sPulling the backups of %s to %s.\n", stamp(), p.Meta.Name, b.plan.To)
	start := time.Now()
	err = repo.Pull(ctx, from, b.plan.Keep)
	var snaps []backup.Snapshot
	if err == nil {
		snaps, err = repo.Snapshots(ctx)
	}
	if rerr := record(p, "backup pull", "", start, err); rerr != nil && err == nil {
		err = rerr
	}
	if err != nil {
		return fmt.Errorf("the pull failed: %w; nothing on this machine was lost", err)
	}
	st, err := backup.LoadState(p.Dir)
	if err != nil {
		return err
	}
	st.Pulled = start
	st.Saw(snaps)
	if err := backup.SaveState(p.Dir, st); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "%s%s\n", stamp(), describeState(st))
	return nil
}

var errNoAnswer = errors.New("the server does not answer")

// serverSource is the repository at path on the server of p, through the
// tunnel; in a terminal it waits for the tunnel, scheduled it does not.
func serverSource(ctx context.Context, s *streams, p *project.Project, srv *manifest.Server, path string, scheduled bool) (backup.Source, error) {
	cfg, err := p.Config()
	if err != nil {
		return backup.Source{}, err
	}
	address, err := manifest.Render("server.tunnel", srv.Tunnel, cfg, nil)
	if err != nil {
		return backup.Source{}, fmt.Errorf("server.tunnel of the stack: %w", err)
	}
	public, err := manifest.Render("server.address", srv.Address, cfg, nil)
	if err != nil {
		return backup.Source{}, fmt.Errorf("server.address of the stack: %w", err)
	}
	user, err := manifest.Render("server.ops_user", srv.OpsUser, cfg, nil)
	if err != nil {
		return backup.Source{}, fmt.Errorf("server.ops_user of the stack: %w", err)
	}
	if address == "" || user == "" {
		return backup.Source{}, errors.New("the stack names no tunnel address or ops user of the server to pull the backups from")
	}
	knownHosts := filepath.Join(p.Dir, project.KnownHosts)
	if scheduled {
		if err := login.SameServer(ctx, knownHosts, public, net.JoinHostPort(address, "22")); err != nil {
			if errors.Is(err, login.ErrOtherServer) {
				return backup.Source{}, fmt.Errorf("%w at %s: another server answers there, most likely through the tunnel of another project; the next pull tries again", errNoAnswer, address)
			}
			on, known := login.WireGuardOn(ctx)
			if hint := login.TunnelHint(on, public, known); hint != "" {
				return backup.Source{}, fmt.Errorf("%w at %s; the next pull tries again. %s", errNoAnswer, address, hint)
			}
			return backup.Source{}, fmt.Errorf("%w at %s; the next pull tries again", errNoAnswer, address)
		}
	} else if err := waitTunnel(ctx, s, p, srv); err != nil {
		return backup.Source{}, err
	}
	key, err := sshKey()
	if err != nil {
		return backup.Source{}, err
	}
	return backup.SFTP(user, address, path, knownHosts, key), nil
}

func describeState(st backup.State) string {
	if st.Snapshots == 0 {
		return "There are no snapshots on this machine yet."
	}
	return fmt.Sprintf("%d snapshots on this machine, the latest from %s (%s).", st.Snapshots,
		st.Latest.Local().Format("2006-01-02 15:04"), ago(st.Latest))
}

func backupStatus(ctx context.Context, s *streams, p *project.Project) error {
	b, err := loadBackup(ctx, p)
	if err != nil {
		return err
	}
	fmt.Fprintf(s.out, "The backups of %s: the server makes them, this machine keeps a copy at %s.\n", p.Meta.Name, b.plan.To)
	st, err := backup.LoadState(p.Dir)
	if err != nil {
		return err
	}
	repo, err := b.repo(ctx, s)
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
	fmt.Fprintln(s.out, describeState(st))
	if entries, err := p.History(); err == nil {
		for i := len(entries) - 1; i >= 0; i-- {
			if e := entries[i]; e.Command == "backup pull" {
				fmt.Fprintf(s.out, "The last pull: %s, %s.\n", e.Time.Local().Format("2006-01-02 15:04"), e.Result)
				if e.Error != "" {
					fmt.Fprintf(s.out, "  %s\n", firstLine(e.Error))
				}
				break
			}
		}
	}
	switch {
	case b.plan.Every == 0:
		fmt.Fprintf(s.out, "Pulls run only by hand: damstack backup pull %s. stack.yaml says how often they run by themselves.\n", p.Meta.Name)
	case backup.Scheduled(ctx, p.Meta.Name):
		fmt.Fprintf(s.out, "Pulls run by themselves every %s while this machine is on; what they do is in %s.\n", backup.FormatEvery(b.plan.Every), backup.Where(p.Meta.Name))
	default:
		fmt.Fprintf(s.out, "Pulls should run every %s but are not scheduled: damstack backup schedule %s.\n", backup.FormatEvery(b.plan.Every), p.Meta.Name)
	}
	for _, warn := range []string{staleWarning(st), kitWarning(p, st)} {
		if warn != "" {
			fmt.Fprintln(s.out)
			fmt.Fprintln(s.out, warn)
		}
	}
	return nil
}

// staleWarning says what to do when the latest snapshot on this machine is
// old, or there is none.
func staleWarning(st backup.State) string {
	switch {
	case st.Snapshots == 0:
		return "Nothing is backed up on this machine yet: if the server is lost, so is its data. damstack backup pull copies the backups here."
	case time.Since(st.Latest) > backup.Stale:
		return fmt.Sprintf("The latest backup on this machine is from %s: either the server stopped backing up, or the pulls fail. damstack backup pull says which.", ago(st.Latest))
	}
	return ""
}

// scheduleBackup makes the pulls of a project run as stack.yaml says; loud
// says so even when nothing changed.
func scheduleBackup(ctx context.Context, s *streams, p *project.Project, b *projectBackup, loud bool) error {
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
		fmt.Fprintf(s.out, "Backups of %s are pulled only by hand: damstack backup pull %s.\n", p.Meta.Name, p.Meta.Name)
	case b.plan.Every > 0 && (changed || loud):
		fmt.Fprintf(s.out, "Backups of %s are pulled to %s every %s while this machine is on; damstack backup status shows them.\n",
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
