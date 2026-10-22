package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "silo.db"), 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestImagesAndContainers(t *testing.T) {
	s := open(t)
	img := &Image{ID: "img1", Reference: "alpine:3.20", Digest: "sha256:x", Source: "registry", Rootfs: "/r", CreatedAt: time.Now(),
		Config: ImageConfig{Cmd: []string{"/bin/sh"}}}
	if err := s.PutImage(img); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetImage("img")
	if err != nil || got.Reference != "alpine:3.20" || got.Config.Cmd[0] != "/bin/sh" {
		t.Fatalf("obtido %+v %v", got, err)
	}
	c := &Container{ID: "abc123def456", Name: "web", ImageID: "img1", ImageRef: img.Reference, State: StateCreated, CreatedAt: time.Now(),
		Spec: Spec{Image: "alpine:3.20", Ports: []PortMapping{{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}}}
	if err := s.CreateContainer(c); err != nil {
		t.Fatal(err)
	}
	dup := *c
	dup.ID = "outro"
	if err := s.CreateContainer(&dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("nome repetido deveria dar conflito: %v", err)
	}
	if err := s.DeleteImage("img1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("imagem em uso não deveria ser removida: %v", err)
	}
	if name, _ := s.PortInUse(8080, "tcp", ""); name != "" {
		t.Fatal("porta de container parado não deveria contar")
	}
	now := time.Now()
	c.State = StateRunning
	c.Pid = 42
	c.IP = "10.88.0.2"
	c.StartedAt = &now
	if err := s.UpdateContainer(c); err != nil {
		t.Fatal(err)
	}
	if name, _ := s.PortInUse(8080, "tcp", "x"); name != "web" {
		t.Fatalf("porta em uso não detectada: %q", name)
	}
	ips, _ := s.UsedIPs()
	if !ips["10.88.0.2"] {
		t.Fatal("IP em uso não listado")
	}
	byPrefix, err := s.GetContainer("abc1")
	if err != nil || byPrefix.Name != "web" || byPrefix.StartedAt == nil {
		t.Fatalf("busca por prefixo falhou: %+v %v", byPrefix, err)
	}
	counts, _ := s.Counts()
	if counts.Running != 1 || counts.Images != 1 {
		t.Fatalf("contagem errada: %+v", counts)
	}
	if err := s.DeleteContainer(c.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteImage("img1"); err != nil {
		t.Fatal(err)
	}
}

func TestEvents(t *testing.T) {
	s := open(t)
	for i := 0; i < 5; i++ {
		s.AddEvent(Event{Type: "container", Action: "start", ActorID: "a", ActorName: "web", Attributes: map[string]string{"n": "1"}})
	}
	s.AddEvent(Event{Type: "image", Action: "pull", ActorID: "b"})
	all, _ := s.Events(0, "", 3)
	if len(all) != 3 || all[2].Action != "pull" {
		t.Fatalf("últimos eventos errados: %+v", all)
	}
	after, _ := s.Events(all[0].ID, "a", 10)
	for _, e := range after {
		if e.ActorID != "a" {
			t.Fatal("filtro por alvo falhou")
		}
	}
	last, _ := s.LastEventID()
	if last != 6 {
		t.Fatalf("último ID %d", last)
	}
}
