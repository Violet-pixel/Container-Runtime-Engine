package runtime

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Violet-pixel/Container-Runtime-Engine/internal/cgroups"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/config"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/network"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/store"
)

type ExecOptions struct {
	Cmd     []string
	Env     []string
	WorkDir string
	User    string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
}

func (r *Runtime) Exec(ref string, opts ExecOptions) (int, error) {
	c, err := r.Get(ref)
	if err != nil {
		return 125, err
	}
	if c.State != store.StateRunning || !alive(c.Pid, c.PidStart) {
		return 125, ErrNotRunning
	}
	if len(opts.Cmd) == 0 {
		return 125, fmt.Errorf("%w: informe o comando", ErrInvalid)
	}
	nsenter, err := exec.LookPath("nsenter")
	if err != nil {
		return 125, fmt.Errorf("nsenter não encontrado (instale util-linux)")
	}
	img, _ := r.Store.GetImage(c.ImageID)
	env := []string{"PATH=" + defaultPath}
	if img != nil {
		env = mergeEnv(env, img.Config.Env)
	}
	env = mergeEnv(env, []string{"HOSTNAME=" + c.Spec.Hostname, "container=silo", "TERM=xterm"})
	env = mergeEnv(env, c.Spec.Env)
	env = mergeEnv(env, opts.Env)
	args := []string{"__enter", c.ID, nsenter, "-t", strconv.Itoa(c.Pid), "-m", "-u", "-i", "-p", "-C", "-r"}
	if c.Spec.Network != network.ModeHost {
		args = append(args, "-n")
	}
	if opts.WorkDir != "" {
		args = append(args, "--wd="+opts.WorkDir)
	} else {
		args = append(args, "-w")
	}
	user := opts.User
	if user == "" {
		user = c.Spec.User
	}
	if user != "" && user != "root" && user != "0" {
		uid, gid, _ := strings.Cut(user, ":")
		if _, err := strconv.Atoi(uid); err != nil {
			return 125, fmt.Errorf("%w: no exec use UID numérico (ex.: 1000:1000)", ErrInvalid)
		}
		if gid == "" {
			gid = uid
		}
		args = append(args, "-S", uid, "-G", gid)
	}
	args = append(args, "--")
	args = append(args, opts.Cmd...)
	cmd := exec.Command(r.exe, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = opts.Stdin, opts.Stdout, opts.Stderr
	r.event("exec", c, map[string]string{"cmd": strings.Join(opts.Cmd, " ")})
	err = cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), nil
		}
		return 126, err
	}
	return 0, nil
}

func (r *Runtime) Enter(id string, argv []string) error {
	cg := r.cgroup(id)
	if err := cg.AddProc(os.Getpid()); err != nil {
		return fmt.Errorf("cgroup: %w", err)
	}
	if len(argv) == 0 {
		return fmt.Errorf("comando vazio")
	}
	return syscall.Exec(argv[0], argv, os.Environ())
}

func TailLines(path string, n int) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := fi.Size()
	if n <= 0 {
		const max = 16 << 20
		start := int64(0)
		if size > max {
			start = size - max
		}
		f.Seek(start, io.SeekStart)
		b, err := io.ReadAll(f)
		return b, size, err
	}
	var buf []byte
	chunk := int64(64 * 1024)
	pos := size
	for pos > 0 {
		step := chunk
		if pos < step {
			step = pos
		}
		pos -= step
		part := make([]byte, step)
		if _, err := f.ReadAt(part, pos); err != nil && err != io.EOF {
			return nil, 0, err
		}
		buf = append(part, buf...)
		if bytes.Count(buf, []byte("\n")) > n {
			break
		}
	}
	lines := bytes.SplitAfter(buf, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return bytes.Join(lines, nil), size, nil
}

func (r *Runtime) Logs(ctx context.Context, ref string, tail int, follow bool, w io.Writer) error {
	c, err := r.Get(ref)
	if err != nil {
		return err
	}
	path := r.LogPath(c.ID)
	data, offset, err := TailLines(path, tail)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if fl, ok := w.(interface{ Flush() }); ok {
		fl.Flush()
	}
	if !follow {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	f.Seek(offset, io.SeekStart)
	buf := make([]byte, 32*1024)
	idle := 0
	for {
		n, err := f.Read(buf)
		if n > 0 {
			idle = 0
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if fl, ok := w.(interface{ Flush() }); ok {
				fl.Flush()
			}
			continue
		}
		if err != nil && err != io.EOF {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(250 * time.Millisecond):
		}
		idle++
		if idle%8 == 0 {
			cur, err := r.Store.GetContainer(c.ID)
			if err != nil || (cur.State != store.StateRunning && cur.State != store.StateStopping) {
				n, _ := f.Read(buf)
				if n > 0 {
					w.Write(buf[:n])
				}
				return nil
			}
		}
	}
}

type Stats struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Time       time.Time        `json:"time"`
	CPUPercent float64          `json:"cpuPercent"`
	CPULimit   float64          `json:"cpuLimit"`
	MemPercent float64          `json:"memoryPercent"`
	Cgroup     cgroups.Stats    `json:"cgroup"`
	Network    network.Counters `json:"network"`
}

func (r *Runtime) Stats(ref string) (*Stats, error) {
	c, err := r.Get(ref)
	if err != nil {
		return nil, err
	}
	if c.State != store.StateRunning {
		return nil, ErrNotRunning
	}
	cs, err := r.cgroup(c.ID).Stats()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	s := &Stats{ID: c.ID, Name: c.Name, Time: now, Cgroup: cs, Network: network.ReadCounters(c.Pid), CPULimit: c.Spec.Resources.CPUs}
	r.mu.Lock()
	prev, ok := r.samples[c.ID]
	r.samples[c.ID] = cpuSample{usage: cs.CPUUsageUsec, at: now}
	r.mu.Unlock()
	if ok && cs.CPUUsageUsec >= prev.usage {
		elapsed := now.Sub(prev.at).Microseconds()
		if elapsed > 0 {
			s.CPUPercent = float64(cs.CPUUsageUsec-prev.usage) / float64(elapsed) * 100
		}
	}
	limit := cs.MemoryLimit
	if limit == 0 || limit > hostMemory() {
		limit = hostMemory()
	}
	if limit > 0 {
		s.MemPercent = float64(cs.MemoryUsage) / float64(limit) * 100
	}
	return s, nil
}

func (r *Runtime) SampleStats(ref string, interval time.Duration) (*Stats, error) {
	if _, err := r.Stats(ref); err != nil {
		return nil, err
	}
	time.Sleep(interval)
	return r.Stats(ref)
}

type Process struct {
	Pid     int    `json:"pid"`
	PPid    int    `json:"ppid"`
	State   string `json:"state"`
	UID     string `json:"uid"`
	RSSKB   int64  `json:"rssKb"`
	Threads int    `json:"threads"`
	Command string `json:"command"`
}

func (r *Runtime) Top(ref string) ([]Process, error) {
	c, err := r.Get(ref)
	if err != nil {
		return nil, err
	}
	if c.State != store.StateRunning {
		return nil, ErrNotRunning
	}
	pids, err := r.cgroup(c.ID).Procs()
	if err != nil {
		return nil, err
	}
	sort.Ints(pids)
	out := []Process{}
	for _, pid := range pids {
		p := Process{Pid: pid}
		status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(status))
		for sc.Scan() {
			k, v, _ := strings.Cut(sc.Text(), ":")
			v = strings.TrimSpace(v)
			switch k {
			case "State":
				p.State = v
			case "PPid":
				p.PPid, _ = strconv.Atoi(v)
			case "Uid":
				if f := strings.Fields(v); len(f) > 0 {
					p.UID = f[0]
				}
			case "VmRSS":
				if f := strings.Fields(v); len(f) > 0 {
					p.RSSKB, _ = strconv.ParseInt(f[0], 10, 64)
				}
			case "Threads":
				p.Threads, _ = strconv.Atoi(v)
			}
		}
		cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		p.Command = strings.TrimSpace(strings.ReplaceAll(string(cmdline), "\x00", " "))
		out = append(out, p)
	}
	return out, nil
}

type Inspect struct {
	*store.Container
	Paths struct {
		Dir    string `json:"dir"`
		Rootfs string `json:"rootfs"`
		Log    string `json:"log"`
		Cgroup string `json:"cgroup"`
	} `json:"paths"`
	Namespaces map[string]string `json:"namespaces,omitempty"`
}

func (r *Runtime) Inspect(ref string) (*Inspect, error) {
	c, err := r.Get(ref)
	if err != nil {
		return nil, err
	}
	in := &Inspect{Container: c}
	dir := r.Cfg.ContainerDir(c.ID)
	in.Paths.Dir = dir
	in.Paths.Rootfs = filepath.Join(dir, "rootfs")
	in.Paths.Log = r.LogPath(c.ID)
	in.Paths.Cgroup = r.cgroup(c.ID).Path()
	if c.State == store.StateRunning && c.Pid > 0 {
		in.Namespaces = map[string]string{}
		for _, ns := range []string{"pid", "mnt", "uts", "ipc", "net", "cgroup"} {
			if link, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/%s", c.Pid, ns)); err == nil {
				in.Namespaces[ns] = link
			}
		}
	}
	return in, nil
}

type SystemInfo struct {
	Version       string       `json:"version"`
	Kernel        string       `json:"kernel"`
	Arch          string       `json:"arch"`
	CgroupVersion int          `json:"cgroupVersion"`
	Storage       string       `json:"storageDriver"`
	Root          string       `json:"root"`
	Bridge        string       `json:"bridge"`
	Subnet        string       `json:"subnet"`
	CPUs          int          `json:"cpus"`
	MemoryBytes   uint64       `json:"memoryBytes"`
	Counts        store.Counts `json:"counts"`
	Hostname      string       `json:"hostname"`
	Time          time.Time    `json:"time"`
}

func (r *Runtime) Info() (*SystemInfo, error) {
	r.Reconcile()
	counts, err := r.Store.Counts()
	if err != nil {
		return nil, err
	}
	var uts syscall.Utsname
	syscall.Uname(&uts)
	host, _ := os.Hostname()
	return &SystemInfo{
		Version: config.Version, Kernel: utsString(uts.Release), Arch: goruntime.GOARCH,
		CgroupVersion: cgroups.Version(), Storage: r.StorageDriver(), Root: r.Cfg.Root,
		Bridge: r.Cfg.BridgeName, Subnet: r.Bridge.Subnet.String(), CPUs: goruntime.NumCPU(),
		MemoryBytes: hostMemory(), Counts: counts, Hostname: host, Time: time.Now(),
	}, nil
}

func utsString[T int8 | uint8](a [65]T) string {
	b := make([]byte, 0, 65)
	for _, c := range a {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}

func hostMemory() uint64 {
	var si syscall.Sysinfo_t
	if syscall.Sysinfo(&si) != nil {
		return 0
	}
	return uint64(si.Totalram) * uint64(si.Unit)
}
