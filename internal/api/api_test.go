package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Violet-pixel/Container-Runtime-Engine/internal/config"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/runtime"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/store"
)

func newServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	cfg := config.Load()
	cfg.Root = t.TempDir()
	cfg.Storage = "copy"
	if err := cfg.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath(), 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.PutImage(&store.Image{ID: "i1", Reference: "teste:1", Rootfs: t.TempDir(), CreatedAt: time.Now(), Config: store.ImageConfig{Cmd: []string{"/bin/sh"}}})
	rt, err := runtime.New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{rt: rt, opts: Options{Token: token}, log: nil}
	s.log = discardLogger()
	srv := httptest.NewServer(s.Router())
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url, token, body string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res, out
}

func TestAuthAndErrors(t *testing.T) {
	srv := newServer(t, "segredo")
	if res, _ := do(t, "GET", srv.URL+"/api/v1/info", "", ""); res.StatusCode != 401 {
		t.Fatalf("sem token: %d", res.StatusCode)
	}
	if res, _ := do(t, "GET", srv.URL+"/api/v1/info", "errado", ""); res.StatusCode != 401 {
		t.Fatalf("token errado: %d", res.StatusCode)
	}
	res, body := do(t, "GET", srv.URL+"/api/v1/info", "segredo", "")
	if res.StatusCode != 200 || body["version"] != config.Version {
		t.Fatalf("info: %d %v", res.StatusCode, body)
	}
	if res, _ := do(t, "GET", srv.URL+"/healthz", "", ""); res.StatusCode != 200 {
		t.Fatal("healthz deveria ser público")
	}
	res, body = do(t, "GET", srv.URL+"/api/v1/containers/nada", "segredo", "")
	if res.StatusCode != 404 || body["error"].(map[string]any)["code"] != "not_found" {
		t.Fatalf("404 esperado: %d %v", res.StatusCode, body)
	}
}

func TestCreateValidation(t *testing.T) {
	srv := newServer(t, "")
	cases := []string{
		`{"image":"teste:1","network":"wifi","start":false}`,
		`{"image":"teste:1","resources":{"memoryMb":1},"start":false}`,
		`{"image":"teste:1","campoInexistente":1}`,
		`{"image":"teste:1","name":"nome inválido!","start":false}`,
	}
	for _, c := range cases {
		if res, body := do(t, "POST", srv.URL+"/api/v1/containers", "", c); res.StatusCode != 400 {
			t.Fatalf("%s: esperado 400, obtido %d %v", c, res.StatusCode, body)
		}
	}
	res, body := do(t, "POST", srv.URL+"/api/v1/containers", "", `{"image":"teste:1","name":"web","start":false,"resources":{"cpus":0.5,"memoryMb":64}}`)
	if res.StatusCode != 201 {
		t.Fatalf("criação: %d %v", res.StatusCode, body)
	}
	if res, _ := do(t, "POST", srv.URL+"/api/v1/containers", "", `{"image":"teste:1","name":"web","start":false}`); res.StatusCode != 409 {
		t.Fatalf("nome repetido: %d", res.StatusCode)
	}
	if res, _ := do(t, "POST", srv.URL+"/api/v1/containers/web/stop", "", ""); res.StatusCode != 409 {
		t.Fatalf("parar container parado: %d", res.StatusCode)
	}
	res, body = do(t, "GET", srv.URL+"/api/v1/containers/web", "", "")
	if res.StatusCode != 200 || body["state"] != "created" {
		t.Fatalf("inspect: %d %v", res.StatusCode, body)
	}
	if res, _ := do(t, "DELETE", srv.URL+"/api/v1/images/teste:1", "", ""); res.StatusCode != 409 {
		t.Fatalf("imagem em uso: %d", res.StatusCode)
	}
	if res, _ := do(t, "DELETE", srv.URL+"/api/v1/containers/web", "", ""); res.StatusCode != 200 {
		t.Fatalf("remoção: %d", res.StatusCode)
	}
}

func TestUIAndOpenAPI(t *testing.T) {
	srv := newServer(t, "")
	for _, p := range []string{"/", "/qualquer/rota", "/api/openapi.yaml"} {
		res, err := http.Get(srv.URL + p)
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("%s: %v %v", p, res.StatusCode, err)
		}
		res.Body.Close()
	}
}
