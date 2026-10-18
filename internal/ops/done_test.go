package ops

import (
	"reflect"
	"testing"

	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/event"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/plan"
)

func TestAdminPagesAreFoundByTheirConsulNames(t *testing.T) {
	p, _ := newProject(t, "name: demo\n")
	m := &manifest.Manifest{Name: "hashi", Tokens: map[string]string{"nomad": "nomad_token", "vault": "vault_token"},
		Done: []string{"Nomad: https://nomad.service.consul:4646, Vault: https://vault.service.consul:8200.",
			"Grafana: https://grafana.service.consul", "The shop: https://shop.example.com"}}
	want := []event.Link{
		{URL: "https://nomad.service.consul:4646", Admin: true, Token: "nomad"},
		{URL: "https://vault.service.consul:8200", Admin: true, Token: "vault"},
		{URL: "https://grafana.service.consul", Admin: true},
		{URL: "https://shop.example.com"},
	}
	if got := RunDone(p, m).Links; !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestAdminPagesAreFoundUnderTheInfraDomain(t *testing.T) {
	p, _ := newProject(t, "name: demo\ninfra_domain: admin.example.com\n")
	m := &manifest.Manifest{Name: "hashi", Tokens: map[string]string{"consul": "consul_token"},
		Done: []string{"https://consul.admin.example.com and https://shop.example.com", "https://traefik.admin.example.com."}}
	want := []event.Link{
		{URL: "https://consul.admin.example.com", Admin: true, Token: "consul"},
		{URL: "https://traefik.admin.example.com", Admin: true},
		{URL: "https://shop.example.com"},
	}
	if got := RunDone(p, m).Links; !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	config, _ := p.Config()
	for host, admin := range map[string]bool{"nomad.service.consul": true, "consul.admin.example.com": true,
		"admin.example.com": false, "example.com": false, "notadmin.example.com": false, "consul": false} {
		if _, ok := AdminHost(config, m, host); ok != admin {
			t.Errorf("%s: admin %v", host, ok)
		}
	}
}

func TestADomainOfTheCatalogProvidesWhatAnAppRequires(t *testing.T) {
	stacks := []config.Stack{
		{Name: "hashi", Path: "platforms/hashi"},
		{Name: "cloudflare", Path: "domains/cloudflare", Kind: manifest.KindDomain, Platform: "hashi", Provides: []string{"public-dns@1"}, Recommended: true},
		{Name: "manual", Path: "domains/manual", Kind: manifest.KindDomain, Platform: "hashi", Provides: []string{"public-dns@1"}},
		{Name: "traefik", Path: "apps/traefik", Kind: manifest.KindApp, Platform: "hashi", Provides: []string{"ingress-public@1"},
			Requires: []manifest.Need{{Capability: "public-dns@1"}}},
	}
	catalog := CatalogModules(stacks)
	var names []string
	for _, m := range catalog {
		names = append(names, m.Name)
	}
	if !reflect.DeepEqual(names, []string{"cloudflare", "manual", "traefik"}) {
		t.Fatalf("the catalog: %v", names)
	}
	pl, err := plan.Resolve(plan.Input{Root: plan.Module{Name: "site", Requires: []manifest.Need{{Capability: "ingress-public@1"}}},
		Platform: "hashi", Catalog: catalog})
	if err != nil || !reflect.DeepEqual(pl.Names(), []string{"cloudflare", "traefik", "site"}) {
		t.Errorf("got %v, %v", pl.Names(), err)
	}
}

func TestTheTunnelAnswersDNSOnlyWhenTheStackSaysSo(t *testing.T) {
	p, _ := newProject(t, "network: {cidr: 10.77.0.0/24}\n")
	m := &manifest.Manifest{Name: "hashi", Server: &manifest.Server{WireGuard: &manifest.WireGuard{Network: "{{ .config.network.cidr }}"}}}
	if dns, err := TunnelDNS(p, m); err != nil || dns != "" {
		t.Errorf("without dns: %q, %v", dns, err)
	}
	m.Server.WireGuard.DNS = true
	if dns, err := TunnelDNS(p, m); err != nil || dns != "10.77.0.1" {
		t.Errorf("with dns: %q, %v", dns, err)
	}
	if dns, err := TunnelDNS(p, &manifest.Manifest{Name: "bare"}); err != nil || dns != "" {
		t.Errorf("without a server: %q, %v", dns, err)
	}
}
