package login

import (
	"strings"
	"testing"
)

const ncList = `Available network connection services in the current set (*=enabled):
* (Disconnected)   33A1D44F VPN (com.nordvpn.NordVPN) "NordVPN - NordLynx"             [VPN:com.nordvpn.NordVPN]
* (Disconnected)   3D23CA29 VPN (com.wireguard.macos) "laptop"                         [VPN:com.wireguard.macos]
* (Connected)      25A7922F VPN (com.wireguard.macos) "demo"                           [VPN:com.wireguard.macos]
`

func TestConnected(t *testing.T) {
	got := connected(ncList)
	if len(got) != 1 || got[0].id != "25A7922F" || got[0].name != "demo" {
		t.Errorf("got %+v", got)
	}
	show := "* (Connected) 25A7922F VPN (com.wireguard.macos) \"demo\"\n  RemoteAddress : 35.205.60.25:51820\n"
	if r := remote(show); r != "35.205.60.25:51820" {
		t.Errorf("remote: %q", r)
	}
}

func TestTunnelHint(t *testing.T) {
	if h := TunnelHint(nil, "1.2.3.4", true); !strings.Contains(h, "No tunnel") {
		t.Errorf("none on: %q", h)
	}
	if h := TunnelHint(nil, "1.2.3.4", false); h != "" {
		t.Errorf("unknown: %q", h)
	}
	if h := TunnelHint([]WireGuard{{"demo", "1.2.3.4:51820"}}, "1.2.3.4", true); h != "" {
		t.Errorf("the right one on: %q", h)
	}
	h := TunnelHint([]WireGuard{{"demo", "35.205.60.25:51820"}}, "207.175.243.2", true)
	if !strings.Contains(h, `"demo" is on and goes to 35.205.60.25:51820`) || !strings.Contains(h, "set its Endpoint to 207.175.243.2") {
		t.Errorf("a stale endpoint: %q", h)
	}
}
