package ops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/skip2/go-qrcode"

	"github.com/eugene-panin/damstack/internal/login"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/sshkey"
	"github.com/eugene-panin/damstack/internal/wg"
)

// WaitTunnel returns once the server answers over the private network; in a
// terminal it asks to turn the tunnel on, and waits.
func WaitTunnel(ctx context.Context, ui UI, p *project.Project, srv *manifest.Server) error {
	config, err := p.Config()
	if err != nil {
		return err
	}
	address, err := manifest.Render("server.tunnel", srv.Tunnel, config, nil)
	if err != nil {
		return fmt.Errorf("server.tunnel of the stack: %w", err)
	}
	help, err := manifest.Render("server.tunnel_help", srv.TunnelHelp, config, nil)
	if err != nil {
		return fmt.Errorf("server.tunnel_help of the stack: %w", err)
	}
	public, err := manifest.RenderLogin("server.address", srv.Address, config, nil)
	if err != nil {
		return fmt.Errorf("server.address of the stack: %w", err)
	}
	knownHosts := filepath.Join(p.Dir, project.KnownHosts)
	shown := false
	for {
		err := login.SameServer(ctx, knownHosts, public, net.JoinHostPort(address, "22"))
		if err == nil {
			return nil
		}
		msg := fmt.Sprintf("This step reaches the server through the private network, and %s does not answer.", address)
		if errors.Is(err, login.ErrOtherServer) {
			return fmt.Errorf("this step reaches the server through the private network, and at %s answers another "+
				"server than %s, most likely through the tunnel of another project. Nothing was changed. Turn that "+
				"tunnel off, and this project's on. %s", address, public, help)
		}
		on, known := login.WireGuardOn(ctx)
		hint := login.TunnelHint(on, public, known)
		if ui.Terminal() == nil {
			for _, more := range []string{hint, help} {
				if more != "" {
					msg += " " + more
				}
			}
			return errors.New(msg)
		}
		if !shown {
			devices(ui.Out(), p, srv, help)
			shown = true
		} else {
			fmt.Fprintf(ui.Out(), "  %s does not answer yet.\n", address)
		}
		if hint != "" {
			fmt.Fprintf(ui.Out(), "  %s\n", hint)
		}
		if _, err := ui.Line("Press Enter once the tunnel is on: "); err != nil {
			return err
		}
	}
}

// devices says how the devices join the private network: the configuration
// to import on this computer, and a QR code for each other one.
func devices(w io.Writer, p *project.Project, srv *manifest.Server, help string) {
	fmt.Fprintln(w, "\n── Your devices join the private network")
	if srv.WireGuard != nil {
		config, _ := p.Config()
		names, _ := project.List(config, srv.WireGuard.Devices)
		for i, name := range names {
			if i == 0 {
				fmt.Fprintf(w, "This Mac: damstack tunnel show %s puts it into the WireGuard app; turn it on.\n", name)
				continue
			}
			fmt.Fprintf(w, "%s: damstack tunnel show %s --qr shows a QR code to scan in its WireGuard app.\n", name, name)
		}
		fmt.Fprintln(w)
		return
	}
	var configs []string
	if srv.TunnelConfigs != "" {
		configs, _ = filepath.Glob(filepath.Join(p.Dir, srv.TunnelConfigs))
	}
	if len(configs) == 0 {
		fmt.Fprintln(w, help)
		return
	}
	fmt.Fprintf(w, "This computer: import %s into the WireGuard app, and turn it on.\n", configs[0])
	for _, config := range configs[1:] {
		data, err := os.ReadFile(config)
		if err != nil {
			continue
		}
		code, err := qrcode.New(string(data), qrcode.Low)
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(config), filepath.Ext(config))
		fmt.Fprintf(w, "\n%s: scan this in the WireGuard app, or import %s\n%s", name, config, code.ToSmallString(false))
	}
	fmt.Fprintln(w)
}

// EnsureKeys gives the devices of a project the keys they lack, before a
// deploy or a check of its stack.
func EnsureKeys(p *project.Project, m *manifest.Manifest, password string) (wg.Change, error) {
	if _, err := EnsureSSHKey(p, m, password); err != nil {
		return wg.Change{}, err
	}
	if m.Server == nil || m.Server.WireGuard == nil {
		return wg.Change{}, nil
	}
	t, err := loadTunnel(nil, false, p, m, "", password)
	if err != nil {
		return wg.Change{}, err
	}
	return t.reconcile()
}

// Tunnel is the private network of a project, with its keys.
type Tunnel struct {
	ui       UI
	yes      bool
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

// OpenTunnel is the private network of p, which the stack m in dir sets up;
// it reaches the person through ui, and with yes applies without asking.
func OpenTunnel(ui UI, yes bool, p *project.Project, m *manifest.Manifest, dir string) (*Tunnel, error) {
	password, err := p.Password()
	if err != nil {
		return nil, err
	}
	return loadTunnel(ui, yes, p, m, dir, password)
}

func loadTunnel(ui UI, yes bool, p *project.Project, m *manifest.Manifest, dir, password string) (*Tunnel, error) {
	if m.Server == nil || m.Server.WireGuard == nil {
		return nil, fmt.Errorf("the stack %s of %s keeps no WireGuard keys of its own", m.Name, p.Meta.Name)
	}
	t := &Tunnel{ui: ui, yes: yes, p: p, m: m, dir: dir, w: m.Server.WireGuard, password: password}
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
	if t.public, err = manifest.RenderLogin("server.address", m.Server.Address, config, nil); err != nil {
		return nil, err
	}
	if t.user, err = manifest.RenderLogin("server.ops_user", m.Server.OpsUser, config, nil); err != nil {
		return nil, err
	}
	return t, nil
}

// Names are the devices stack.yaml lists.
func (t *Tunnel) Names() []string { return t.names }

// reconcile gives keys to the devices stack.yaml lists that have none, and
// takes them from those it no longer lists, and saves them.
func (t *Tunnel) reconcile() (wg.Change, error) {
	c, err := t.state.Reconcile(t.names, t.network, time.Now())
	if err != nil || !c.Any() {
		return c, err
	}
	return c, t.save()
}

func (t *Tunnel) save() error {
	v, err := t.state.Secret()
	if err != nil {
		return err
	}
	t.secrets[t.w.Secret] = v
	return t.p.SaveSecrets(t.password, t.secrets)
}

// apply sets the keys on the server, over its public address.
func (t *Tunnel) apply(ctx context.Context) error {
	step := t.m.Commands[t.w.Apply]
	e, _, err := newEngine(ctx, t.ui, t.yes, t.p, t.m, t.dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(t.ui.Out(), "Setting the keys on the server at %s.\n", t.public)
	start := time.Now()
	err = e.Run(ctx, step, nil)
	if rerr := Record(t.p, "tunnel "+t.w.Apply, "", start, err); rerr != nil && err == nil {
		err = rerr
	}
	return err
}

// Add lets a new device onto the private network.
func (t *Tunnel) Add(ctx context.Context, name string) error {
	if slices.Contains(t.names, name) {
		return fmt.Errorf("%s is a device of %s already", name, t.p.Meta.Name)
	}
	return t.setDevices(ctx, append(t.names, name))
}

// Remove takes a device off the private network, once the person agrees.
func (t *Tunnel) Remove(ctx context.Context, name string) error {
	if !slices.Contains(t.names, name) {
		return fmt.Errorf("%s is not a device of %s; damstack tunnel list shows them", name, t.p.Meta.Name)
	}
	ok, err := t.ui.Confirm(fmt.Sprintf("%s loses its access to %s at once. Go?", name, t.p.Meta.Name), false)
	if err != nil || !ok {
		return err
	}
	return t.setDevices(ctx, slices.DeleteFunc(slices.Clone(t.names), func(n string) bool { return n == name }))
}

func (t *Tunnel) setDevices(ctx context.Context, names []string) error {
	if err := t.p.SetList(t.w.Devices, names); err != nil {
		return err
	}
	t.names = names
	c, err := t.reconcile()
	if err != nil {
		return err
	}
	if err := t.apply(ctx); err != nil {
		if len(c.Removed) > 0 {
			return fmt.Errorf("%w; %s left stack.yaml, but the server still lets %s in until damstack deploy %s sets its keys there",
				err, strings.Join(c.Removed, ", "), map[bool]string{true: "them", false: "it"}[len(c.Removed) > 1], t.p.Meta.Name)
		}
		return fmt.Errorf("%w; damstack deploy %s sets the keys on the server", err, t.p.Meta.Name)
	}
	for _, name := range c.Removed {
		fmt.Fprintf(t.ui.Out(), "%s is off the private network; its keys no longer work.\n", name)
	}
	for _, name := range c.Added {
		fmt.Fprintf(t.ui.Out(), "%s is on the private network at %s. Give it its configuration: damstack tunnel show %s, with --qr for a phone.\n",
			name, t.state.Devices[name].Address, name)
	}
	return nil
}

// Rotate gives new keys to the device name, or every device, or the server,
// once the person agrees, and sets them on the server.
func (t *Tunnel) Rotate(ctx context.Context, name string, all, server bool) error {
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
	ok, err := t.ui.Confirm(fmt.Sprintf("New keys for %s: the old ones stop working at once, and %s need%s the new configuration. Go?",
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
	fmt.Fprintf(t.ui.Out(), "New keys for %s are on the server. Each device needs its new configuration:\n", what)
	for _, d := range devices {
		fmt.Fprintf(t.ui.Out(), "  damstack tunnel show %s\n", d)
	}
	if len(devices) == 1 && t.ui.Terminal() != nil {
		return t.Show(ctx, devices[0], false, false)
	}
	return nil
}

// Show gives a device its configuration: into the WireGuard app of this Mac,
// as a QR code with qr, or printed with print.
func (t *Tunnel) Show(ctx context.Context, name string, qr, print bool) error {
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
		fmt.Fprint(t.ui.Out(), conf)
		return nil
	case qr:
		code, err := qrcode.New(conf, qrcode.Low)
		if err != nil {
			return err
		}
		fmt.Fprintf(t.ui.Out(), "%s: in the WireGuard app of the phone, add a tunnel from a QR code, and scan this.\n%s", name, code.ToSmallString(false))
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
	fmt.Fprintf(t.ui.Out(), "The WireGuard app imports the tunnel %s: allow it, then turn the tunnel on.\n", t.p.Meta.Name)
	fmt.Fprintln(t.ui.Out(), "If a tunnel of that name is there already, remove it in the app first: the old keys no longer work.")
	if !t.imported(ctx, 15*time.Second) {
		copy := exec.CommandContext(ctx, "pbcopy")
		copy.Stdin = strings.NewReader(conf)
		if err := copy.Run(); err != nil {
			return fmt.Errorf("the WireGuard app did not take the tunnel, and the clipboard does not either: %w; damstack tunnel show %s --print prints it", err, name)
		}
		fmt.Fprintf(t.ui.Out(), "\nThe app did not take it. The configuration is on the clipboard instead: in the WireGuard app, click +,\n"+
			"then Add Empty Tunnel, select all in the text, paste, name it %s, and save. Then clear the clipboard: it holds the private key.\n", t.p.Meta.Name)
	}
	if t.ui.Terminal() == nil {
		time.Sleep(5 * time.Second)
		return nil
	}
	if _, err := t.ui.Line("Press Enter once the tunnel is on: "); err != nil {
		return err
	}
	return t.waitHandshake(ctx, name)
}

// imported is whether the WireGuard app holds a tunnel named after the
// project, or does so within wait.
func (t *Tunnel) imported(ctx context.Context, wait time.Duration) bool {
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
func (t *Tunnel) waitHandshake(ctx context.Context, name string) error {
	d := t.state.Devices[name]
	since := time.Now().Add(-3 * time.Minute)
	for range 12 {
		seen, err := t.handshakes(ctx)
		if err != nil {
			return fmt.Errorf("the server does not say whether %s connected: %w", name, err)
		}
		if at, ok := seen[d.PublicKey]; ok && at.After(since) {
			fmt.Fprintf(t.ui.Out(), "%s is connected with its new keys.\n", name)
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
func (t *Tunnel) handshakes(ctx context.Context) (map[string]time.Time, error) {
	key, err := KeyFor(t.p, t.m, t.password)
	if err != nil {
		return nil, err
	}
	iface := t.w.Interface
	if iface == "" {
		iface = "wg0"
	}
	out, err := exec.CommandContext(ctx, "ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", sshkey.KnownHostsOption(filepath.Join(t.p.Dir, project.KnownHosts)), "-o", "StrictHostKeyChecking=accept-new",
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

// List lists the devices: their addresses, how old their keys are, when each
// last connected.
func (t *Tunnel) List(ctx context.Context) error {
	seen, err := t.handshakes(ctx)
	if err != nil {
		fmt.Fprintf(t.ui.Err(), "The server does not say when the devices last connected: %v\n", err)
	}
	w := tabwriter.NewWriter(t.ui.Out(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "DEVICE\tADDRESS\tKEYS MADE\tLAST CONNECTED")
	for _, name := range t.names {
		d, ok := t.state.Devices[name]
		if !ok {
			fmt.Fprintf(w, "%s\t-\tnone yet: damstack deploy %s makes them\t\n", name, t.p.Meta.Name)
			continue
		}
		last := "never"
		if at, ok := seen[d.PublicKey]; ok {
			last = Ago(at)
		} else if err != nil {
			last = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, d.Address, d.Created.Local().Format("2006-01-02"), last)
	}
	return w.Flush()
}
