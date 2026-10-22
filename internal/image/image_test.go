package image

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

type entry struct {
	name, body, link string
	typ              byte
}

func layer(t *testing.T, gz bool, entries ...entry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	var tw *tar.Writer
	var zw *gzip.Writer
	if gz {
		zw = gzip.NewWriter(&buf)
		tw = tar.NewWriter(zw)
	} else {
		tw = tar.NewWriter(&buf)
	}
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o644, Size: int64(len(e.body)), Linkname: e.link}
		if e.typ == tar.TypeDir {
			h.Mode = 0o755
			h.Size = 0
		}
		if e.typ == tar.TypeSymlink || e.typ == tar.TypeLink {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	if zw != nil {
		zw.Close()
	}
	return &buf
}

func TestApplyLayersWithWhiteouts(t *testing.T) {
	root := t.TempDir()
	base := layer(t, true,
		entry{name: "etc/", typ: tar.TypeDir},
		entry{name: "etc/a.conf", body: "a", typ: tar.TypeReg},
		entry{name: "etc/b.conf", body: "b", typ: tar.TypeReg},
		entry{name: "var/cache/", typ: tar.TypeDir},
		entry{name: "var/cache/x", body: "x", typ: tar.TypeReg},
		entry{name: "bin/sh", body: "#!", typ: tar.TypeReg},
		entry{name: "bin/ash", link: "sh", typ: tar.TypeSymlink},
		entry{name: "bin/hard", link: "bin/sh", typ: tar.TypeLink},
	)
	if _, err := ApplyLayer(root, base); err != nil {
		t.Fatal(err)
	}
	upper := layer(t, false,
		entry{name: "etc/.wh.a.conf", typ: tar.TypeReg},
		entry{name: "var/cache/.wh..wh..opq", typ: tar.TypeReg},
		entry{name: "var/cache/novo", body: "n", typ: tar.TypeReg},
		entry{name: "etc/b.conf", body: "bb", typ: tar.TypeReg},
	)
	if _, err := ApplyLayer(root, upper); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/a.conf")); !os.IsNotExist(err) {
		t.Fatal("whiteout não removeu etc/a.conf")
	}
	if _, err := os.Stat(filepath.Join(root, "var/cache/x")); !os.IsNotExist(err) {
		t.Fatal("diretório opaco não foi limpo")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "etc/b.conf")); string(b) != "bb" {
		t.Fatalf("arquivo não sobrescrito: %q", b)
	}
	if l, _ := os.Readlink(filepath.Join(root, "bin/ash")); l != "sh" {
		t.Fatalf("symlink errado: %q", l)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "bin/hard")); string(b) != "#!" {
		t.Fatal("hardlink não criado")
	}
}

func TestApplyLayerBlocksEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "escapou")
	l := layer(t, false, entry{name: "../escapou", body: "x", typ: tar.TypeReg})
	ApplyLayer(root, l)
	if _, err := os.Stat(outside); err == nil {
		os.Remove(outside)
		t.Fatal("arquivo escrito fora do rootfs")
	}
	root2 := t.TempDir()
	l2 := layer(t, false,
		entry{name: "link", link: "/tmp", typ: tar.TypeSymlink},
		entry{name: "link/silo-escape-test", body: "x", typ: tar.TypeReg},
	)
	if _, err := ApplyLayer(root2, l2); err == nil {
		t.Fatal("escrita através de symlink deveria falhar")
	}
	if _, err := os.Stat("/tmp/silo-escape-test"); err == nil {
		os.Remove("/tmp/silo-escape-test")
		t.Fatal("arquivo escrito fora do rootfs via symlink")
	}
}

func TestParseReference(t *testing.T) {
	cases := map[string]string{
		"alpine":               "alpine:latest",
		"alpine:3.20":          "alpine:3.20",
		"library/nginx:alpine": "nginx:alpine",
		"docker.io/redis:7":    "redis:7",
		"ghcr.io/org/app:1.0":  "ghcr.io/org/app:1.0",
		"localhost:5000/teste": "localhost:5000/teste:latest",
		"busybox@sha256:abc":   "busybox@sha256:abc",
	}
	for in, want := range cases {
		r, err := ParseReference(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if r.String() != want {
			t.Fatalf("%s: obtido %s, esperado %s", in, r.String(), want)
		}
	}
	r, _ := ParseReference("alpine")
	if r.Registry != "registry-1.docker.io" || r.Repository != "library/alpine" {
		t.Fatalf("referência padrão errada: %+v", r)
	}
	if _, err := ParseReference(""); err == nil {
		t.Fatal("referência vazia aceita")
	}
}

func TestParseChallenge(t *testing.T) {
	p := parseChallenge(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/alpine:pull"`)
	if p["realm"] != "https://auth.docker.io/token" || p["service"] != "registry.docker.io" || p["scope"] != "repository:library/alpine:pull" {
		t.Fatalf("obtido %v", p)
	}
}

func TestPickPlatform(t *testing.T) {
	os.Setenv("SILO_ARCH", "arm64")
	defer os.Unsetenv("SILO_ARCH")
	list := []descriptor{
		{Digest: "amd", Platform: &struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
			Variant      string `json:"variant"`
		}{"amd64", "linux", ""}},
		{Digest: "arm", Platform: &struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
			Variant      string `json:"variant"`
		}{"arm64", "linux", "v8"}},
	}
	d, err := pickPlatform(list)
	if err != nil || d.Digest != "arm" {
		t.Fatalf("obtido %+v %v", d, err)
	}
}
