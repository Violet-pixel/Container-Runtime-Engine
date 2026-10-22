package runtime

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Violet-pixel/Container-Runtime-Engine/internal/cgroups"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/config"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/image"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/names"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/network"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/store"
)

const defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

var (
	ErrNotRunning     = errors.New("o container não está em execução")
	ErrAlreadyRunning = errors.New("o container já está em execução")
	ErrInvalid        = errors.New("parâmetro inválido")
)

type Runtime struct {
	Cfg    config.Config
	Store  *store.Store
	Images *image.Service
	Bridge *network.Bridge
	exe    string

	mu      sync.Mutex
	samples map[string]cpuSample
	storage string
}

type cpuSample struct {
	usage uint64
	at    time.Time
}

func New(cfg config.Config, st *store.Store) (*Runtime, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	br, err := network.ParseBridge(cfg.BridgeName, cfg.BridgeCIDR)
	if err != nil {
		return nil, err
	}
	return &Runtime{
		Cfg: cfg, Store: st, Images: image.NewService(cfg, st), Bridge: br, exe: exe,
		samples: map[string]cpuSample{},
	}, nil
}

func (r *Runtime) event(action string, c *store.Container, attrs map[string]string) {
	r.Store.AddEvent(store.Event{Type: "container", Action: action, ActorID: c.ID, ActorName: c.Name, Attributes: attrs})
}

func (r *Runtime) cgroup(id string) cgroups.Manager { return cgroups.New("silo/" + id) }

func (r *Runtime) StorageDriver() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.storage != "" {
		return r.storage
	}
	switch r.Cfg.Storage {
	case "overlay", "copy":
		r.storage = r.Cfg.Storage
	default:
		r.storage = "copy"
		if overlaySupported(r.Cfg.TmpDir()) {
			r.storage = "overlay"
		}
	}
	return r.storage
}

func overlaySupported(tmp string) bool {
	dir, err := os.MkdirTemp(tmp, "ovl-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	for _, d := range []string{"l", "u", "w", "m"} {
		os.Mkdir(filepath.Join(dir, d), 0o755)
	}
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", filepath.Join(dir, "l"), filepath.Join(dir, "u"), filepath.Join(dir, "w"))
	if err := syscall.Mount("overlay", filepath.Join(dir, "m"), "overlay", 0, opts); err != nil {
		return false
	}
	syscall.Unmount(filepath.Join(dir, "m"), syscall.MNT_DETACH)
	return true
}

func mergeEnv(base, extra []string) []string {
	order := []string{}
	vals := map[string]string{}
	for _, list := range [][]string{base, extra} {
		for _, kv := range list {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				continue
			}
			if _, seen := vals[k]; !seen {
				order = append(order, k)
			}
			vals[k] = v
		}
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+vals[k])
	}
	return out
}

func validate(spec *store.Spec) error {
	if spec.Network == "" {
		spec.Network = network.ModeBridge
	}
	if !network.ValidMode(spec.Network) {
		return fmt.Errorf("%w: rede deve ser bridge, host ou none", ErrInvalid)
	}
	if spec.Resources.CPUs < 0 || spec.Resources.CPUs > float64(goruntime.NumCPU()) {
		return fmt.Errorf("%w: cpus deve estar entre 0 e %d", ErrInvalid, goruntime.NumCPU())
	}
	if spec.Resources.MemoryMB < 0 || (spec.Resources.MemoryMB > 0 && spec.Resources.MemoryMB < 6) {
		return fmt.Errorf("%w: memória mínima de 6 MB", ErrInvalid)
	}
	if spec.Resources.PidsLimit < 0 {
		return fmt.Errorf("%w: limite de processos negativo", ErrInvalid)
	}
	if spec.Resources.CPUWeight < 0 || spec.Resources.CPUWeight > 10000 {
		return fmt.Errorf("%w: peso de CPU deve estar entre 1 e 10000", ErrInvalid)
	}
	switch spec.Restart.Name {
	case "", "no":
		spec.Restart.Name = "no"
	case "always", "on-failure":
	default:
		return fmt.Errorf("%w: política de reinício deve ser no, on-failure ou always", ErrInvalid)
	}
	if spec.AutoRemove && spec.Restart.Name != "no" {
		return fmt.Errorf("%w: --rm não combina com política de reinício", ErrInvalid)
	}
	seen := map[string]bool{}
	for i := range spec.Ports {
		p := &spec.Ports[i]
		if p.Protocol == "" {
			p.Protocol = "tcp"
		}
		if p.Protocol != "tcp" && p.Protocol != "udp" {
			return fmt.Errorf("%w: protocolo %q", ErrInvalid, p.Protocol)
		}
		if p.HostPort < 1 || p.HostPort > 65535 || p.ContainerPort < 1 || p.ContainerPort > 65535 {
			return fmt.Errorf("%w: porta fora do intervalo 1-65535", ErrInvalid)
		}
		if p.HostIP == "" {
			p.HostIP = "0.0.0.0"
		}
		if net.ParseIP(p.HostIP) == nil {
			return fmt.Errorf("%w: IP %q", ErrInvalid, p.HostIP)
		}
		key := fmt.Sprintf("%d/%s", p.HostPort, p.Protocol)
		if seen[key] {
			return fmt.Errorf("%w: porta %s repetida", ErrInvalid, key)
		}
		seen[key] = true
	}
	if len(spec.Ports) > 0 && spec.Network != network.ModeBridge {
		return fmt.Errorf("%w: publicar portas exige a rede bridge", ErrInvalid)
	}
	for _, m := range spec.Mounts {
		if !filepath.IsAbs(m.Source) || !filepath.IsAbs(m.Target) {
			return fmt.Errorf("%w: volumes precisam de caminhos absolutos (%s:%s)", ErrInvalid, m.Source, m.Target)
		}
		if _, err := os.Stat(m.Source); err != nil {
			return fmt.Errorf("%w: origem do volume %s não existe", ErrInvalid, m.Source)
		}
	}
	if spec.Hostname != "" && !names.Valid(spec.Hostname) {
		return fmt.Errorf("%w: hostname %q", ErrInvalid, spec.Hostname)
	}
	return nil
}

func (r *Runtime) Create(spec store.Spec, name string) (*store.Container, error) {
	if err := validate(&spec); err != nil {
		return nil, err
	}
	img, err := r.Images.Lookup(spec.Image)
	if err != nil {
		return nil, err
	}
	if name == "" {
		for i := 0; i < 10; i++ {
			name = names.Random()
			if _, err := r.Store.GetContainer(name); errors.Is(err, store.ErrNotFound) {
				break
			}
		}
	} else if !names.Valid(name) {
		return nil, fmt.Errorf("%w: nome %q (use letras, números, _ . -)", ErrInvalid, name)
	}
	entry := img.Config.Entrypoint
	cmd := img.Config.Cmd
	if len(spec.Entrypoint) > 0 {
		entry = spec.Entrypoint
		cmd = nil
	}
	if len(spec.Cmd) > 0 {
		cmd = spec.Cmd
	}
	command := append(append([]string{}, entry...), cmd...)
	if len(command) == 0 {
		return nil, fmt.Errorf("%w: a imagem não define comando; informe um", ErrInvalid)
	}
	id := names.ID()
	if spec.Hostname == "" {
		spec.Hostname = id[:12]
	}
	if spec.WorkingDir == "" {
		spec.WorkingDir = img.Config.WorkingDir
	}
	if spec.User == "" {
		spec.User = img.Config.User
	}
	c := &store.Container{
		ID: id, Name: name, ImageID: img.ID, ImageRef: img.Reference, Spec: spec, Command: command,
		State: store.StateCreated, Storage: r.StorageDriver(), CreatedAt: time.Now(),
	}
	dir := r.Cfg.ContainerDir(id)
	if err := os.MkdirAll(dir, 0o711); err != nil {
		return nil, err
	}
	if c.Storage == "overlay" {
		for _, d := range []string{"upper", "work", "rootfs"} {
			os.MkdirAll(filepath.Join(dir, d), 0o755)
		}
	} else {
		if out, err := exec.Command("cp", "-a", img.Rootfs, filepath.Join(dir, "rootfs")).CombinedOutput(); err != nil {
			os.RemoveAll(dir)
			return nil, fmt.Errorf("copiar rootfs: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	os.WriteFile(filepath.Join(dir, "container.log"), nil, 0o640)
	if err := r.Store.CreateContainer(c); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	r.event("create", c, map[string]string{"image": c.ImageRef})
	return c, nil
}

func (r *Runtime) Get(ref string) (*store.Container, error) {
	r.Reconcile()
	return r.Store.GetContainer(ref)
}

func (r *Runtime) List(all bool) ([]store.Container, error) {
	r.Reconcile()
	return r.Store.ListContainers(all)
}

func procStart(pid int) uint64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	s := string(b)
	i := strings.LastIndex(s, ")")
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return 0
	}
	n, _ := strconv.ParseUint(f[19], 10, 64)
	return n
}

func alive(pid int, start uint64) bool {
	if pid <= 0 {
		return false
	}
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	if start != 0 && procStart(pid) != start {
		return false
	}
	b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if i := strings.LastIndex(string(b), ")"); i > 0 {
		f := strings.Fields(string(b)[i+1:])
		if len(f) > 0 && f[0] == "Z" {
			return false
		}
	}
	return true
}

func (r *Runtime) Reconcile() {
	list, err := r.Store.ListContainers(false)
	if err != nil {
		return
	}
	for i := range list {
		c := &list[i]
		if alive(c.Pid, c.PidStart) || (c.ShimPid > 0 && alive(c.ShimPid, 0)) {
			continue
		}
		now := time.Now()
		c.State = store.StateExited
		c.Pid = 0
		c.ShimPid = 0
		c.ExitCode = 255
		c.Error = "o processo terminou sem supervisor (reinício da máquina ou do serviço)"
		c.FinishedAt = &now
		r.Store.UpdateContainer(c)
		r.cgroup(c.ID).Destroy()
		network.Detach(c.ID)
		r.event("die", c, map[string]string{"exitCode": "255", "reason": "orphan"})
	}
}

func (r *Runtime) Start(ref string) (*store.Container, error) {
	c, err := r.Get(ref)
	if err != nil {
		return nil, err
	}
	if c.State == store.StateRunning || c.State == store.StateStopping {
		return nil, ErrAlreadyRunning
	}
	if err := network.Requirements(c.Spec.Network); err != nil {
		return nil, err
	}
	if err := r.checkPorts(c); err != nil {
		return nil, err
	}
	requested := time.Now()
	c.StopRequested = false
	c.Error = ""
	r.Store.UpdateContainer(c)
	logf, err := os.OpenFile(filepath.Join(r.Cfg.ContainerDir(c.ID), "shim.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	cmd := exec.Command(r.exe, "shim", c.ID)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Env = append(os.Environ(), "SILO_ROOT="+r.Cfg.Root)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	shimPid := cmd.Process.Pid
	go cmd.Wait()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(60 * time.Millisecond)
		cur, err := r.Store.GetContainer(c.ID)
		if err != nil {
			return nil, err
		}
		if cur.State == store.StateRunning && cur.Pid > 0 {
			return cur, nil
		}
		if cur.State == store.StateExited && cur.FinishedAt != nil && cur.FinishedAt.After(requested) {
			if cur.Error != "" {
				return cur, errors.New(cur.Error)
			}
			return cur, nil
		}
		if !alive(shimPid, 0) {
			cur, _ = r.Store.GetContainer(c.ID)
			if cur != nil && cur.State == store.StateRunning {
				return cur, nil
			}
			b, _ := os.ReadFile(filepath.Join(r.Cfg.ContainerDir(c.ID), "shim.log"))
			return cur, fmt.Errorf("o supervisor terminou antes do container iniciar: %s", lastLine(string(b)))
		}
	}
	return nil, errors.New("tempo esgotado esperando o container iniciar")
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func (r *Runtime) checkPorts(c *store.Container) error {
	for _, p := range c.Spec.Ports {
		if other, err := r.Store.PortInUse(p.HostPort, p.Protocol, c.ID); err != nil {
			return err
		} else if other != "" {
			return fmt.Errorf("a porta %d/%s já está em uso pelo container %s", p.HostPort, p.Protocol, other)
		}
	}
	return nil
}

func (r *Runtime) signal(c *store.Container, sig syscall.Signal) error {
	if !alive(c.Pid, c.PidStart) {
		return ErrNotRunning
	}
	return syscall.Kill(c.Pid, sig)
}

func (r *Runtime) Kill(ref string, sig syscall.Signal) (*store.Container, error) {
	c, err := r.Get(ref)
	if err != nil {
		return nil, err
	}
	if c.State != store.StateRunning && c.State != store.StateStopping {
		return c, ErrNotRunning
	}
	if sig == syscall.SIGKILL || sig == syscall.SIGTERM {
		r.Store.SetStopRequested(c.ID, true)
	}
	if err := r.signal(c, sig); err != nil {
		return c, err
	}
	r.event("kill", c, map[string]string{"signal": strconv.Itoa(int(sig))})
	return c, nil
}

func (r *Runtime) waitExit(id string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := r.Store.GetContainer(id)
		if err != nil || c.State == store.StateExited || c.State == store.StateCreated {
			return true
		}
		if !alive(c.Pid, c.PidStart) && !alive(c.ShimPid, 0) {
			r.Reconcile()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func (r *Runtime) Stop(ref string, timeout time.Duration) (*store.Container, error) {
	c, err := r.Get(ref)
	if err != nil {
		return nil, err
	}
	if c.State != store.StateRunning && c.State != store.StateStopping {
		return c, ErrNotRunning
	}
	if timeout <= 0 {
		timeout = time.Duration(r.Cfg.StopTimeout) * time.Second
	}
	c.StopRequested = true
	c.State = store.StateStopping
	r.Store.UpdateContainer(c)
	r.event("stop", c, nil)
	_ = r.signal(c, syscall.SIGTERM)
	if !r.waitExit(c.ID, timeout) {
		_ = r.signal(c, syscall.SIGKILL)
		r.waitExit(c.ID, 10*time.Second)
	}
	return r.Store.GetContainer(c.ID)
}

func (r *Runtime) Restart(ref string, timeout time.Duration) (*store.Container, error) {
	c, err := r.Get(ref)
	if err != nil {
		return nil, err
	}
	if c.State == store.StateRunning || c.State == store.StateStopping {
		if _, err := r.Stop(c.ID, timeout); err != nil && !errors.Is(err, ErrNotRunning) {
			return nil, err
		}
	}
	return r.Start(c.ID)
}

func (r *Runtime) Remove(ref string, force bool) (*store.Container, error) {
	c, err := r.Get(ref)
	if err != nil {
		return nil, err
	}
	if c.State == store.StateRunning || c.State == store.StateStopping {
		if !force {
			return nil, fmt.Errorf("%w: o container está em execução (pare antes ou use --force)", store.ErrConflict)
		}
		r.Store.SetStopRequested(c.ID, true)
		_ = r.signal(c, syscall.SIGKILL)
		r.waitExit(c.ID, 10*time.Second)
	}
	return c, r.destroy(c)
}

func (r *Runtime) destroy(c *store.Container) error {
	if err := r.Store.DeleteContainer(c.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	r.cgroup(c.ID).Destroy()
	network.Detach(c.ID)
	dir := r.Cfg.ContainerDir(c.ID)
	syscall.Unmount(filepath.Join(dir, "rootfs"), syscall.MNT_DETACH)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	r.mu.Lock()
	delete(r.samples, c.ID)
	r.mu.Unlock()
	r.event("destroy", c, nil)
	return nil
}

func (r *Runtime) Prune() ([]string, error) {
	list, err := r.List(true)
	if err != nil {
		return nil, err
	}
	var removed []string
	for i := range list {
		if list[i].State == store.StateExited || list[i].State == store.StateCreated {
			if err := r.destroy(&list[i]); err == nil {
				removed = append(removed, list[i].Name)
			}
		}
	}
	return removed, nil
}

func (r *Runtime) LogPath(id string) string {
	return filepath.Join(r.Cfg.ContainerDir(id), "container.log")
}
