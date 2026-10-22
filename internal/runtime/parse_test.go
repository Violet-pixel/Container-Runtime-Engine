package runtime

import (
	"os"
	"testing"

	"github.com/Violet-pixel/Container-Runtime-Engine/internal/store"
)

func TestParsePort(t *testing.T) {
	cases := []struct {
		in        string
		host, ctr int
		ip, proto string
		ok        bool
	}{
		{"8080:80", 8080, 80, "0.0.0.0", "tcp", true},
		{"53:53/udp", 53, 53, "0.0.0.0", "udp", true},
		{"127.0.0.1:9000:9000", 9000, 9000, "127.0.0.1", "tcp", true},
		{"3000", 3000, 3000, "0.0.0.0", "tcp", true},
		{"abc:80", 0, 0, "", "", false},
		{"x.y:1:2", 0, 0, "", "", false},
	}
	for _, c := range cases {
		p, err := ParsePort(c.in)
		if (err == nil) != c.ok {
			t.Fatalf("%s: erro inesperado %v", c.in, err)
		}
		if c.ok && (p.HostPort != c.host || p.ContainerPort != c.ctr || p.HostIP != c.ip || p.Protocol != c.proto) {
			t.Fatalf("%s: obtido %+v", c.in, p)
		}
	}
}

func TestParseMount(t *testing.T) {
	m, err := ParseMount("/srv:/dados:ro")
	if err != nil || m.Source != "/srv" || m.Target != "/dados" || !m.ReadOnly {
		t.Fatalf("obtido %+v %v", m, err)
	}
	if _, err := ParseMount("/srv:/dados:xx"); err == nil {
		t.Fatal("modo inválido aceito")
	}
	if _, err := ParseMount("/srv"); err == nil {
		t.Fatal("volume sem destino aceito")
	}
}

func TestParseMemory(t *testing.T) {
	cases := map[string]int64{"256m": 256, "1g": 1024, "512MB": 512, "": 0, "2048k": 2}
	for in, want := range cases {
		got, err := ParseMemory(in)
		if err != nil || got != want {
			t.Fatalf("%q: obtido %d (%v), esperado %d", in, got, err, want)
		}
	}
	if _, err := ParseMemory("muito"); err == nil {
		t.Fatal("valor inválido aceito")
	}
}

func TestParseRestart(t *testing.T) {
	rp, err := ParseRestart("on-failure:3")
	if err != nil || rp.Name != "on-failure" || rp.MaxRetries != 3 {
		t.Fatalf("obtido %+v %v", rp, err)
	}
	if _, err := ParseRestart("always:x"); err == nil {
		t.Fatal("máximo inválido aceito")
	}
}

func TestMergeEnv(t *testing.T) {
	got := mergeEnv([]string{"PATH=/bin", "A=1"}, []string{"A=2", "B=3", "invalido"})
	want := []string{"PATH=/bin", "A=2", "B=3"}
	if len(got) != len(want) {
		t.Fatalf("obtido %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("obtido %v", got)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := store.Spec{Image: "x", Ports: []store.PortMapping{{HostPort: 80, ContainerPort: 80}}}
	if err := validate(&ok); err != nil {
		t.Fatal(err)
	}
	if ok.Network != "bridge" || ok.Restart.Name != "no" || ok.Ports[0].Protocol != "tcp" {
		t.Fatalf("padrões não aplicados: %+v", ok)
	}
	bad := []store.Spec{
		{Network: "wifi"},
		{Network: "host", Ports: []store.PortMapping{{HostPort: 80, ContainerPort: 80}}},
		{Resources: store.Resources{MemoryMB: 2}},
		{Resources: store.Resources{CPUs: 9999}},
		{Restart: store.RestartPolicy{Name: "sempre"}},
		{AutoRemove: true, Restart: store.RestartPolicy{Name: "always"}},
		{Mounts: []store.Mount{{Source: "relativo", Target: "/x"}}},
		{Ports: []store.PortMapping{{HostPort: 80, ContainerPort: 80}, {HostPort: 80, ContainerPort: 81}}},
	}
	for i, s := range bad {
		if err := validate(&s); err == nil {
			t.Fatalf("caso %d deveria falhar: %+v", i, s)
		}
	}
	dir := t.TempDir()
	vol := store.Spec{Mounts: []store.Mount{{Source: dir, Target: "/dados"}}}
	if err := validate(&vol); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(dir)
	if err := validate(&vol); err == nil {
		t.Fatal("volume inexistente aceito")
	}
}
