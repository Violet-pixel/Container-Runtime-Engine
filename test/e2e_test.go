package test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var bin string

func TestMain(m *testing.M) {
	if os.Getenv("SILO_E2E") != "1" || os.Geteuid() != 0 {
		os.Exit(0)
	}
	dir, _ := os.MkdirTemp("", "silo-e2e-")
	bin = filepath.Join(dir, "silo")
	build := exec.Command("go", "build", "-o", bin, "../cmd/silo")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		os.Exit(1)
	}
	os.Setenv("SILO_ROOT", filepath.Join(dir, "root"))
	os.Setenv("SILO_BRIDGE", "siloe2e0")
	os.Setenv("SILO_BRIDGE_CIDR", "10.199.0.1/24")
	code := m.Run()
	exec.Command(bin, "rm", "-f", "e2e-web", "e2e-lim").Run()
	exec.Command("ip", "link", "del", "siloe2e0").Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func silo(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("silo %v: %v", args, err)
	}
	return strings.TrimSpace(out.String()), code
}

func TestLifecycle(t *testing.T) {
	rootfs := os.Getenv("SILO_E2E_ROOTFS")
	if rootfs == "" {
		t.Skip("defina SILO_E2E_ROOTFS (make rootfs)")
	}
	if out, code := silo(t, "import", rootfs, "e2e:1", "/bin/sh"); code != 0 {
		t.Fatalf("import: %s", out)
	}

	out, code := silo(t, "run", "--rm", "--hostname", "isolado", "e2e:1", "sh", "-c", "hostname; echo $$; ls /proc | grep -c '^[0-9]'")
	if code != 0 {
		t.Fatalf("run: %d %s", code, out)
	}
	lines := strings.Split(out, "\n")
	if lines[0] != "isolado" {
		t.Fatalf("namespace UTS não isolado: %q", out)
	}
	if lines[1] != "1" {
		t.Fatalf("o processo deveria ser o PID 1 do namespace: %q", out)
	}

	if _, code := silo(t, "run", "--rm", "e2e:1", "sh", "-c", "exit 7"); code != 7 {
		t.Fatalf("código de saída não repassado: %d", code)
	}

	out, _ = silo(t, "run", "--rm", "--network", "none", "e2e:1", "sh", "-c", "ls /sys/class/net")
	if strings.TrimSpace(out) != "lo" {
		t.Fatalf("rede none deveria ter só lo: %q", out)
	}

	if out, code := silo(t, "run", "-d", "--name", "e2e-lim", "-m", "32m", "--cpus", "0.5", "--pids-limit", "8", "e2e:1", "sh", "-c", "yes > /dev/null"); code != 0 {
		t.Fatalf("run -d: %s", out)
	}
	time.Sleep(3 * time.Second)
	out, _ = silo(t, "inspect", "e2e-lim")
	var info []map[string]any
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("inspect: %v %s", err, out)
	}
	if info[0]["state"] != "running" {
		t.Fatalf("estado: %v", info[0]["state"])
	}
	stats, _ := silo(t, "stats", "--no-stream", "e2e-lim")
	if !strings.Contains(stats, "32.0 MiB") || !strings.Contains(stats, "/ 8") {
		t.Fatalf("limites não aplicados: %s", stats)
	}
	if out, code := silo(t, "exec", "e2e-lim", "sh", "-c", "for i in 1 2 3 4 5 6 7 8 9 10; do sleep 5 & done; wait"); code == 0 {
		t.Fatalf("pids-limit deveria impedir 10 processos: %s", out)
	}
	if out, code := silo(t, "stop", "-t", "2", "e2e-lim"); code != 0 {
		t.Fatalf("stop: %s", out)
	}
	if out, code := silo(t, "rm", "e2e-lim"); code != 0 {
		t.Fatalf("rm: %s", out)
	}

	if out, code := silo(t, "run", "-d", "--name", "e2e-web", "-p", "18999:8000", "e2e:1", "sh", "-c", "sleep 60"); code != 0 {
		t.Fatalf("porta: %s", out)
	}
	if _, code := silo(t, "run", "-d", "--name", "e2e-dup", "-p", "18999:8000", "e2e:1", "sleep", "5"); code == 0 {
		t.Fatal("porta repetida deveria falhar")
	}
	silo(t, "rm", "-f", "e2e-dup")
	silo(t, "rm", "-f", "e2e-web")
}

func TestAPI(t *testing.T) {
	cmd := exec.Command(bin, "serve", "--addr", "127.0.0.1:17979")
	cmd.Start()
	defer cmd.Process.Kill()
	var res *http.Response
	var err error
	for i := 0; i < 40; i++ {
		res, err = http.Get("http://127.0.0.1:17979/api/v1/info")
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "cgroupVersion") {
		t.Fatalf("info: %d %s", res.StatusCode, body)
	}
}
