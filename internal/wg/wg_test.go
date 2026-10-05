package wg

import (
	"strings"
	"testing"
	"time"
)

func TestKeys(t *testing.T) {
	k, err := NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	if pub, err := PublicKey(k.PrivateKey); err != nil || pub != k.PublicKey || len(k.PublicKey) != 44 {
		t.Errorf("%+v: %s, %v", k, pub, err)
	}
	// wg genkey and wg pubkey of a known key
	if pub, _ := PublicKey("UFNV8JN2/NtHDBz0X2VLWFVF8qm924RE+7wveXwE4k4="); pub != "9uKmalcxD8yJeqTGCcIuVxYFhXgHltAjOYDJocWhzz8=" {
		t.Errorf("the public key of a known key: %s", pub)
	}
}

func TestReconcileKeepsAddresses(t *testing.T) {
	s, _ := FromSecret(nil)
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	c, err := s.Reconcile([]string{"laptop", "phone", "tablet"}, "10.77.0.0/24", now)
	if err != nil || !c.Server || strings.Join(c.Added, ",") != "laptop,phone,tablet" {
		t.Fatalf("%+v, %v", c, err)
	}
	if s.Devices["laptop"].Address != "10.77.0.2" || s.Devices["tablet"].Address != "10.77.0.4" {
		t.Errorf("addresses: %+v", s.Devices)
	}
	phone := *s.Devices["phone"]

	c, _ = s.Reconcile([]string{"laptop", "tablet", "watch"}, "10.77.0.0/24", now)
	if c.Server || strings.Join(c.Removed, ",") != "phone" || strings.Join(c.Added, ",") != "watch" {
		t.Errorf("second: %+v", c)
	}
	if s.Devices["tablet"].Address != "10.77.0.4" || s.Devices["watch"].Address != "10.77.0.3" {
		t.Errorf("an address moved, or the freed one was not taken: %+v", s.Devices)
	}
	if s.Devices["watch"].PublicKey == phone.PublicKey {
		t.Error("a new device took the keys of a removed one")
	}
	if c, _ := s.Reconcile([]string{"laptop", "tablet", "watch"}, "10.77.0.0/24", now); c.Any() {
		t.Errorf("nothing changed, yet %+v", c)
	}
}

func TestRotateAndSecret(t *testing.T) {
	s, _ := FromSecret(nil)
	now := time.Now()
	s.Reconcile([]string{"laptop"}, "10.77.0.0/24", now)
	v, err := s.Secret()
	if err != nil {
		t.Fatal(err)
	}
	back, err := FromSecret(v)
	if err != nil || back.Devices["laptop"].PrivateKey != s.Devices["laptop"].PrivateKey || back.Server != s.Server {
		t.Fatalf("round trip: %+v, %v", back, err)
	}
	old := *back.Devices["laptop"]
	if err := back.RotateDevice("laptop", now); err != nil {
		t.Fatal(err)
	}
	d := back.Devices["laptop"]
	if d.PrivateKey == old.PrivateKey || d.PresharedKey == old.PresharedKey || d.Address != old.Address {
		t.Errorf("rotated: %+v, was %+v", d, old)
	}
	if err := back.RotateDevice("phone", now); err == nil {
		t.Error("rotated a device that is not there")
	}
	server := back.Server
	back.RotateServer()
	if back.Server == server {
		t.Error("the server kept its keys")
	}
	conf, err := back.Config("laptop", "203.0.113.5:51820", "10.77.0.0/24")
	for _, want := range []string{"PrivateKey = " + d.PrivateKey, "Address = 10.77.0.2/32", "PublicKey = " + back.Server.PublicKey,
		"PresharedKey = " + d.PresharedKey, "AllowedIPs = 10.77.0.0/24", "Endpoint = 203.0.113.5:51820"} {
		if err != nil || !strings.Contains(conf, want+"\n") {
			t.Errorf("no %q in\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "DNS") {
		t.Error("a DNS server in the configuration")
	}
}
