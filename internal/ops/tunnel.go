package ops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"

	"github.com/eugene-panin/damstack/internal/login"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
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
	w := m.Server.WireGuard
	secrets, err := p.Secrets(password)
	if err != nil {
		return wg.Change{}, err
	}
	state, err := wg.FromSecret(secrets[w.Secret])
	if err != nil {
		return wg.Change{}, err
	}
	config, err := p.Config()
	if err != nil {
		return wg.Change{}, err
	}
	names, err := project.List(config, w.Devices)
	if err != nil {
		return wg.Change{}, err
	}
	var network string
	for field, text := range map[string]string{"network": w.Network, "endpoint": w.Endpoint} {
		out, err := manifest.Render("server.wireguard."+field, text, config, nil)
		if err != nil {
			return wg.Change{}, fmt.Errorf("server.wireguard.%s of the stack: %w", field, err)
		}
		if field == "network" {
			network = out
		}
	}
	if _, err := manifest.RenderLogin("server.address", m.Server.Address, config, nil); err != nil {
		return wg.Change{}, err
	}
	if _, err := manifest.RenderLogin("server.ops_user", m.Server.OpsUser, config, nil); err != nil {
		return wg.Change{}, err
	}
	c, err := state.Reconcile(names, network, time.Now())
	if err != nil || !c.Any() {
		return c, err
	}
	v, err := state.Secret()
	if err != nil {
		return c, err
	}
	secrets[w.Secret] = v
	return c, p.SaveSecrets(password, secrets)
}
