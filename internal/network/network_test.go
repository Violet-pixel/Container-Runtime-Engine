package network

import (
	"strings"
	"testing"
)

func TestAllocate(t *testing.T) {
	b, err := ParseBridge("silo0", "10.88.0.1/16")
	if err != nil {
		t.Fatal(err)
	}
	ip, err := b.Allocate(map[string]bool{"10.88.0.2": true})
	if err != nil || ip.String() != "10.88.0.3" {
		t.Fatalf("obtido %v %v", ip, err)
	}
	small, _ := ParseBridge("x", "192.168.50.1/30")
	if ip, err := small.Allocate(nil); err != nil || ip.String() != "192.168.50.2" {
		t.Fatalf("obtido %v %v", ip, err)
	}
	if _, err := small.Allocate(map[string]bool{"192.168.50.2": true}); err == nil {
		t.Fatal("sub-rede cheia deveria falhar")
	}
	if _, err := ParseBridge("x", "nada"); err == nil {
		t.Fatal("CIDR inválido aceito")
	}
}

func TestVethNames(t *testing.T) {
	h, p := vethNames("0123456789abcdef")
	if len(h) > 15 || len(p) > 15 || h == p {
		t.Fatalf("nomes inválidos %s %s", h, p)
	}
}

func TestResolvConf(t *testing.T) {
	out := string(ResolvConf([]string{"9.9.9.9"}, nil))
	if !strings.Contains(out, "nameserver 9.9.9.9") {
		t.Fatalf("obtido %q", out)
	}
	if strings.Contains(string(ResolvConf(nil, []string{"1.1.1.1"})), "127.0.0.53") {
		t.Fatal("resolver local não deveria ir para o container")
	}
}
