package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/wg"
)

var deviceRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

func tunnelCommand(s *streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tunnel",
		Short: "The devices on the private network of the server, and their WireGuard keys",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list [project]",
		Short: "List the devices: their addresses, how old their keys are, when each last connected",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := openTunnel(cmd.Context(), s, firstArg(args))
			if err != nil {
				return err
			}
			return t.list(cmd.Context())
		},
	})
	var qr, print bool
	show := &cobra.Command{
		Use:   "show <device>",
		Short: "Give a device its configuration: into the WireGuard app of this Mac, or as a QR code for a phone",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := openTunnel(cmd.Context(), s, "")
			if err != nil {
				return err
			}
			return t.show(cmd.Context(), args[0], qr, print)
		},
	}
	show.Flags().BoolVar(&qr, "qr", false, "show a QR code to scan in the WireGuard app of a phone")
	show.Flags().BoolVar(&print, "print", false, "print the configuration, private key included")
	cmd.AddCommand(show)
	cmd.AddCommand(&cobra.Command{
		Use:   "add <device>",
		Short: "Let a new device onto the private network",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !deviceRe.MatchString(args[0]) {
				return fmt.Errorf("%q is not a name for a device: lowercase letters, digits and hyphens, such as phone or work-laptop", args[0])
			}
			t, err := openTunnel(cmd.Context(), s, "")
			if err != nil {
				return err
			}
			if slices.Contains(t.names, args[0]) {
				return fmt.Errorf("%s is a device of %s already", args[0], t.p.Meta.Name)
			}
			return t.setDevices(cmd.Context(), append(t.names, args[0]))
		},
	})
	remove := &cobra.Command{
		Use:   "remove <device>",
		Short: "Take a device off the private network, such as a lost phone; its keys stop working at once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := openTunnel(cmd.Context(), s, "")
			if err != nil {
				return err
			}
			if !slices.Contains(t.names, args[0]) {
				return fmt.Errorf("%s is not a device of %s; damstack tunnel list shows them", args[0], t.p.Meta.Name)
			}
			ok, err := s.confirm(fmt.Sprintf("%s loses its access to %s at once. Go?", args[0], t.p.Meta.Name), false)
			if err != nil || !ok {
				return err
			}
			return t.setDevices(cmd.Context(), slices.DeleteFunc(slices.Clone(t.names), func(n string) bool { return n == args[0] }))
		},
	}
	remove.Flags().BoolVar(&s.yes, "yes", false, "go ahead without asking; needed without a terminal")
	cmd.AddCommand(remove)
	var all, server bool
	rotate := &cobra.Command{
		Use:   "rotate [device]",
		Short: "Give a device new keys, or every device, or the server; the old ones stop working at once",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (len(args) == 1) == (all || server) {
				return &usageError{err: errors.New("name one device, or pass --all or --server"), help: fmt.Sprintf("Run '%s --help' for usage.", cmd.CommandPath())}
			}
			t, err := openTunnel(cmd.Context(), s, "")
			if err != nil {
				return err
			}
			return t.rotate(cmd.Context(), firstArg(args), all, server)
		},
	}
	rotate.Flags().BoolVar(&all, "all", false, "every device")
	rotate.Flags().BoolVar(&s.yes, "yes", false, "go ahead without asking; needed without a terminal")
	rotate.Flags().BoolVar(&server, "server", false, "the server: every device then needs its new configuration")
	cmd.AddCommand(rotate)
	return cmd
}

// tunnel is the private network of a project, with its keys.
type tunnel struct {
	s        *streams
	p        *project.Project
	m        *manifest.Manifest
	dir      string
	w        *manifest.WireGuard
	password string
	secrets  map[string]any
	state    *wg.State
	names    []string
	network  string
	endpoint string
	public   string
	user     string
}

func openTunnel(ctx context.Context, s *streams, name string) (*tunnel, error) {
	p, err := pickProject(s, name)
	if err != nil {
		return nil, err
	}
	m, dir, err := projectStack(ctx, p, "")
	if err != nil {
		return nil, err
	}
	return newTunnel(s, p, m, dir)
}

func newTunnel(s *streams, p *project.Project, m *manifest.Manifest, dir string) (*tunnel, error) {
	password, err := p.Password()
	if err != nil {
		return nil, err
	}
	return loadTunnel(s, p, m, dir, password)
}

// ensureKeys gives the devices of a project the keys they lack, before a
// deploy or a check of its stack.
func ensureKeys(p *project.Project, m *manifest.Manifest, dir, password string) (wg.Change, error) {
	if _, err := ensureSSHKey(p, m, password); err != nil {
		return wg.Change{}, err
	}
	if m.Server == nil || m.Server.WireGuard == nil {
		return wg.Change{}, nil
	}
	t, err := loadTunnel(nil, p, m, dir, password)
	if err != nil {
		return wg.Change{}, err
	}
	return t.reconcile()
}

func loadTunnel(s *streams, p *project.Project, m *manifest.Manifest, dir, password string) (*tunnel, error) {
	if m.Server == nil || m.Server.WireGuard == nil {
		return nil, fmt.Errorf("the stack %s of %s keeps no WireGuard keys of its own", m.Name, p.Meta.Name)
	}
	t := &tunnel{s: s, p: p, m: m, dir: dir, w: m.Server.WireGuard, password: password}
	var err error
	if t.secrets, err = p.Secrets(t.password); err != nil {
		return nil, err
	}
	if t.state, err = wg.FromSecret(t.secrets[t.w.Secret]); err != nil {
		return nil, err
	}
	config, err := p.Config()
	if err != nil {
		return nil, err
	}
	if t.names, err = project.List(config, t.w.Devices); err != nil {
		return nil, err
	}
	for field, out := range map[string]*string{"network": &t.network, "endpoint": &t.endpoint} {
		text := map[string]string{"network": t.w.Network, "endpoint": t.w.Endpoint}[field]
		if *out, err = manifest.Render("server.wireguard."+field, text, config, nil); err != nil {
			return nil, fmt.Errorf("server.wireguard.%s of the stack: %w", field, err)
		}
	}
	if t.public, err = manifest.Render("server.address", m.Server.Address, config, nil); err != nil {
		return nil, err
	}
	if t.user, err = manifest.Render("server.ops_user", m.Server.OpsUser, config, nil); err != nil {
		return nil, err
	}
	return t, nil
}

// reconcile gives keys to the devices stack.yaml lists that have none, and
// takes them from those it no longer lists, and saves them.
func (t *tunnel) reconcile() (wg.Change, error) {
	c, err := t.state.Reconcile(t.names, t.network, time.Now())
	if err != nil || !c.Any() {
		return c, err
	}
	return c, t.save()
}

func (t *tunnel) save() error {
	v, err := t.state.Secret()
	if err != nil {
		return err
	}
	t.secrets[t.w.Secret] = v
	return t.p.SaveSecrets(t.password, t.secrets)
}

// apply sets the keys on the server, over its public address.
func (t *tunnel) apply(ctx context.Context) error {
	step := t.m.Commands[t.w.Apply]
	e, _, err := newEngine(ctx, t.s, t.p, t.m, t.dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(t.s.out, "Setting the keys on the server at %s.\n", t.public)
	start := time.Now()
	err = e.Run(ctx, step, nil)
	if rerr := record(t.p, "tunnel "+t.w.Apply, "", start, err); rerr != nil && err == nil {
		err = rerr
	}
	return err
}

func (t *tunnel) setDevices(ctx context.Context, names []string) error {
	if err := t.p.SetList(t.w.Devices, names); err != nil {
		return err
	}
	t.names = names
	c, err := t.reconcile()
	if err != nil {
		return err
	}
	if err := t.apply(ctx); err != nil {
		return err
	}
	for _, name := range c.Removed {
		fmt.Fprintf(t.s.out, "%s is off the private network; its keys no longer work.\n", name)
	}
	for _, name := range c.Added {
		fmt.Fprintf(t.s.out, "%s is on the private network at %s. Give it its configuration: damstack tunnel show %s, with --qr for a phone.\n",
			name, t.state.Devices[name].Address, name)
	}
	return nil
}

func (t *tunnel) rotate(ctx context.Context, name string, all, server bool) error {
	var devices []string
	what := name
	switch {
	case server:
		devices, what = slices.Clone(t.names), "the server"
	case all:
		devices, what = slices.Clone(t.names), "every device"
	default:
		devices = []string{name}
	}
	ok, err := t.s.confirm(fmt.Sprintf("New keys for %s: the old ones stop working at once, and %s need%s the new configuration. Go?",
		what, strings.Join(devices, ", "), map[bool]string{true: "", false: "s"}[len(devices) > 1]), false)
	if err != nil || !ok {
		return err
	}
	if _, err := t.reconcile(); err != nil {
		return err
	}
	now := time.Now()
	if server {
		if err := t.state.RotateServer(); err != nil {
			return err
		}
	} else {
		for _, d := range devices {
			if err := t.state.RotateDevice(d, now); err != nil {
				return err
			}
		}
	}
	if err := t.save(); err != nil {
		return err
	}
	if err := t.apply(ctx); err != nil {
		return fmt.Errorf("%w; the new keys are in vault.yml, and damstack tunnel rotate or damstack deploy sets them on the server", err)
	}
	fmt.Fprintf(t.s.out, "New keys for %s are on the server. Each device needs its new configuration:\n", what)
	for _, d := range devices {
		fmt.Fprintf(t.s.out, "  damstack tunnel show %s\n", d)
	}
	if len(devices) == 1 && t.s.tty() {
		return t.show(ctx, devices[0], false, false)
	}
	return nil
}

func (t *tunnel) show(ctx context.Context, name string, qr, print bool) error {
	if c, err := t.reconcile(); err != nil {
		return err
	} else if c.Any() {
		return fmt.Errorf("the keys of %s changed since the last deploy; damstack deploy %s sets them on the server first", t.p.Meta.Name, t.p.Meta.Name)
	}
	conf, err := t.state.Config(name, t.endpoint, t.network)
	if err != nil {
		return err
	}
	switch {
	case print:
		fmt.Fprint(t.s.out, conf)
		return nil
	case qr:
		code, err := qrcode.New(conf, qrcode.Low)
		if err != nil {
			return err
		}
		fmt.Fprintf(t.s.out, "%s: in the WireGuard app of the phone, add a tunnel from a QR code, and scan this.\n%s", name, code.ToSmallString(false))
		return nil
	}
	tmp, err := os.MkdirTemp("", "damstack-tunnel-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	file := filepath.Join(tmp, t.p.Meta.Name+".conf")
	if err := os.WriteFile(file, []byte(conf), 0o600); err != nil {
		return err
	}
	if err := exec.CommandContext(ctx, "open", "-a", "WireGuard", file).Run(); err != nil {
		return fmt.Errorf("the WireGuard app does not open: %w; install it from the App Store, or use damstack tunnel show %s --qr", err, name)
	}
	fmt.Fprintf(t.s.out, "The WireGuard app imports the tunnel %s: allow it, then turn the tunnel on.\n", t.p.Meta.Name)
	fmt.Fprintln(t.s.out, "If a tunnel of that name is there already, remove it in the app first: the old keys no longer work.")
	if !t.imported(ctx, 15*time.Second) {
		copy := exec.CommandContext(ctx, "pbcopy")
		copy.Stdin = strings.NewReader(conf)
		if err := copy.Run(); err != nil {
			return fmt.Errorf("the WireGuard app did not take the tunnel, and the clipboard does not either: %w; damstack tunnel show %s --print prints it", err, name)
		}
		fmt.Fprintf(t.s.out, "\nThe app did not take it. The configuration is on the clipboard instead: in the WireGuard app, click +,\n"+
			"then Add Empty Tunnel, select all in the text, paste, name it %s, and save. Then clear the clipboard: it holds the private key.\n", t.p.Meta.Name)
	}
	if !t.s.tty() {
		time.Sleep(5 * time.Second)
		return nil
	}
	if _, err := t.s.prompt.Line("Press Enter once the tunnel is on: "); err != nil {
		return err
	}
	return t.waitHandshake(ctx, name)
}

// imported is whether the WireGuard app holds a tunnel named after the
// project, or does so within wait.
func (t *tunnel) imported(ctx context.Context, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		out, _ := exec.CommandContext(ctx, "scutil", "--nc", "list").Output()
		if strings.Contains(string(out), `"`+t.p.Meta.Name+`"`) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
}

// waitHandshake waits for the server to hear from the device with its
// current key.
func (t *tunnel) waitHandshake(ctx context.Context, name string) error {
	d := t.state.Devices[name]
	since := time.Now().Add(-3 * time.Minute)
	for range 12 {
		seen, err := t.handshakes(ctx)
		if err != nil {
			return fmt.Errorf("the server does not say whether %s connected: %w", name, err)
		}
		if at, ok := seen[d.PublicKey]; ok && at.After(since) {
			fmt.Fprintf(t.s.out, "%s is connected with its new keys.\n", name)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("the server has not heard from %s in a minute: check that the tunnel %s is on in the WireGuard app, "+
		"and that no other VPN, such as NordVPN, is on", name, t.p.Meta.Name)
}

// handshakes are the latest handshakes the server had, by public key, read
// over its public address.
func (t *tunnel) handshakes(ctx context.Context) (map[string]time.Time, error) {
	key, err := keyFor(t.p, t.m, t.password)
	if err != nil {
		return nil, err
	}
	iface := t.w.Interface
	if iface == "" {
		iface = "wg0"
	}
	out, err := exec.CommandContext(ctx, "ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "UserKnownHostsFile="+filepath.Join(t.p.Dir, project.KnownHosts), "-o", "StrictHostKeyChecking=accept-new",
		"-i", key, t.user+"@"+t.public, "sudo", "wg", "show", iface, "latest-handshakes").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("%s", strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, err
	}
	seen := map[string]time.Time{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if sec, err := strconv.ParseInt(fields[1], 10, 64); err == nil && sec > 0 {
			seen[fields[0]] = time.Unix(sec, 0)
		}
	}
	return seen, nil
}

func (t *tunnel) list(ctx context.Context) error {
	seen, err := t.handshakes(ctx)
	if err != nil {
		fmt.Fprintf(t.s.err, "The server does not say when the devices last connected: %v\n", err)
	}
	w := tabwriter.NewWriter(t.s.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "DEVICE\tADDRESS\tKEYS MADE\tLAST CONNECTED")
	for _, name := range t.names {
		d, ok := t.state.Devices[name]
		if !ok {
			fmt.Fprintf(w, "%s\t-\tnone yet: damstack deploy %s makes them\t\n", name, t.p.Meta.Name)
			continue
		}
		last := "never"
		if at, ok := seen[d.PublicKey]; ok {
			last = ago(at)
		} else if err != nil {
			last = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, d.Address, d.Created.Local().Format("2006-01-02"), last)
	}
	return w.Flush()
}
