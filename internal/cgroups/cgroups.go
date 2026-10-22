package cgroups

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const Mountpoint = "/sys/fs/cgroup"

type Limits struct {
	CPUs      float64
	CPUWeight int
	MemoryMB  int64
	PidsLimit int64
}

type Stats struct {
	CPUUsageUsec uint64 `json:"cpuUsageUsec"`
	CPUThrottled uint64 `json:"cpuThrottledUsec"`
	MemoryUsage  uint64 `json:"memoryUsageBytes"`
	MemoryLimit  uint64 `json:"memoryLimitBytes"`
	MemoryPeak   uint64 `json:"memoryPeakBytes"`
	OOMKills     uint64 `json:"oomKills"`
	PidsCurrent  uint64 `json:"pidsCurrent"`
	PidsLimit    uint64 `json:"pidsLimit"`
}

type Manager interface {
	Version() int
	Create(l Limits) error
	Update(l Limits) error
	AddProc(pid int) error
	Procs() ([]int, error)
	Stats() (Stats, error)
	Destroy() error
	Path() string
}

func IsV2() bool {
	_, err := os.Stat(filepath.Join(Mountpoint, "cgroup.controllers"))
	return err == nil
}

func Version() int {
	if IsV2() {
		return 2
	}
	return 1
}

func New(group string) Manager {
	if IsV2() {
		return &v2{rel: group}
	}
	return &v1{rel: group}
}

func writeFile(path, val string) error {
	return os.WriteFile(path, []byte(val), 0o644)
}

func readUint(path string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(b))
	if s == "max" {
		return 0, nil
	}
	return strconv.ParseUint(s, 10, 64)
}

func readKV(path string) map[string]uint64 {
	out := map[string]uint64{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Fields(sc.Text())
		if len(parts) == 2 {
			if v, err := strconv.ParseUint(parts[1], 10, 64); err == nil {
				out[parts[0]] = v
			}
		}
	}
	return out
}

func readProcs(path string) ([]int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []int
	for _, l := range strings.Fields(string(b)) {
		if n, err := strconv.Atoi(l); err == nil {
			out = append(out, n)
		}
	}
	return out, nil
}

func rmdirRetry(path string) error {
	var err error
	for i := 0; i < 20; i++ {
		err = os.Remove(path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return err
}

type v2 struct{ rel string }

func (c *v2) Version() int { return 2 }
func (c *v2) Path() string { return filepath.Join(Mountpoint, c.rel) }

func (c *v2) enableControllers() {
	parts := strings.Split(c.rel, "/")
	cur := Mountpoint
	for _, p := range parts[:len(parts)-1] {
		_ = writeFile(filepath.Join(cur, "cgroup.subtree_control"), "+cpu +memory +pids")
		cur = filepath.Join(cur, p)
		_ = os.MkdirAll(cur, 0o755)
	}
	_ = writeFile(filepath.Join(cur, "cgroup.subtree_control"), "+cpu +memory +pids")
}

func (c *v2) Create(l Limits) error {
	c.enableControllers()
	if err := os.MkdirAll(c.Path(), 0o755); err != nil {
		return fmt.Errorf("cgroup v2: %w", err)
	}
	return c.Update(l)
}

func (c *v2) Update(l Limits) error {
	p := c.Path()
	cpu := "max 100000"
	if l.CPUs > 0 {
		cpu = fmt.Sprintf("%d 100000", int64(l.CPUs*100000))
	}
	if err := writeFile(filepath.Join(p, "cpu.max"), cpu); err != nil && l.CPUs > 0 {
		return fmt.Errorf("limite de CPU: %w", err)
	}
	if l.CPUWeight > 0 {
		if err := writeFile(filepath.Join(p, "cpu.weight"), strconv.Itoa(l.CPUWeight)); err != nil {
			return fmt.Errorf("peso de CPU: %w", err)
		}
	}
	mem := "max"
	if l.MemoryMB > 0 {
		mem = strconv.FormatInt(l.MemoryMB*1024*1024, 10)
	}
	if err := writeFile(filepath.Join(p, "memory.max"), mem); err != nil && l.MemoryMB > 0 {
		return fmt.Errorf("limite de memória: %w", err)
	}
	if l.MemoryMB > 0 {
		_ = writeFile(filepath.Join(p, "memory.swap.max"), "0")
	}
	pids := "max"
	if l.PidsLimit > 0 {
		pids = strconv.FormatInt(l.PidsLimit, 10)
	}
	if err := writeFile(filepath.Join(p, "pids.max"), pids); err != nil && l.PidsLimit > 0 {
		return fmt.Errorf("limite de processos: %w", err)
	}
	return nil
}

func (c *v2) AddProc(pid int) error {
	return writeFile(filepath.Join(c.Path(), "cgroup.procs"), strconv.Itoa(pid))
}

func (c *v2) Procs() ([]int, error) { return readProcs(filepath.Join(c.Path(), "cgroup.procs")) }

func (c *v2) Stats() (Stats, error) {
	p := c.Path()
	if _, err := os.Stat(p); err != nil {
		return Stats{}, err
	}
	var s Stats
	cpu := readKV(filepath.Join(p, "cpu.stat"))
	s.CPUUsageUsec = cpu["usage_usec"]
	s.CPUThrottled = cpu["throttled_usec"]
	s.MemoryUsage, _ = readUint(filepath.Join(p, "memory.current"))
	s.MemoryLimit, _ = readUint(filepath.Join(p, "memory.max"))
	s.MemoryPeak, _ = readUint(filepath.Join(p, "memory.peak"))
	s.OOMKills = readKV(filepath.Join(p, "memory.events"))["oom_kill"]
	s.PidsCurrent, _ = readUint(filepath.Join(p, "pids.current"))
	s.PidsLimit, _ = readUint(filepath.Join(p, "pids.max"))
	return s, nil
}

func (c *v2) Destroy() error {
	if procs, _ := c.Procs(); len(procs) > 0 {
		_ = writeFile(filepath.Join(c.Path(), "cgroup.kill"), "1")
	}
	return rmdirRetry(c.Path())
}

var v1Controllers = []string{"cpu", "cpuacct", "memory", "pids"}

type v1 struct{ rel string }

func (c *v1) Version() int { return 1 }
func (c *v1) Path() string { return c.dir("memory") }

func (c *v1) dir(ctrl string) string {
	base := filepath.Join(Mountpoint, ctrl)
	if ctrl == "cpu" || ctrl == "cpuacct" {
		if _, err := os.Stat(base); err != nil {
			base = filepath.Join(Mountpoint, "cpu,cpuacct")
		}
	}
	return filepath.Join(base, c.rel)
}

func (c *v1) Create(l Limits) error {
	for _, ctrl := range v1Controllers {
		if err := os.MkdirAll(c.dir(ctrl), 0o755); err != nil {
			return fmt.Errorf("cgroup v1 %s: %w", ctrl, err)
		}
	}
	return c.Update(l)
}

func (c *v1) Update(l Limits) error {
	cpu := c.dir("cpu")
	quota := "-1"
	if l.CPUs > 0 {
		quota = strconv.FormatInt(int64(l.CPUs*100000), 10)
	}
	_ = writeFile(filepath.Join(cpu, "cpu.cfs_period_us"), "100000")
	if err := writeFile(filepath.Join(cpu, "cpu.cfs_quota_us"), quota); err != nil && l.CPUs > 0 {
		return fmt.Errorf("limite de CPU: %w", err)
	}
	if l.CPUWeight > 0 {
		shares := 2 + ((l.CPUWeight-1)*262142)/9999
		_ = writeFile(filepath.Join(cpu, "cpu.shares"), strconv.Itoa(shares))
	}
	mem := c.dir("memory")
	limit := "-1"
	if l.MemoryMB > 0 {
		limit = strconv.FormatInt(l.MemoryMB*1024*1024, 10)
	}
	if err := writeFile(filepath.Join(mem, "memory.limit_in_bytes"), limit); err != nil && l.MemoryMB > 0 {
		return fmt.Errorf("limite de memória: %w", err)
	}
	if l.MemoryMB > 0 {
		_ = writeFile(filepath.Join(mem, "memory.memsw.limit_in_bytes"), limit)
	}
	pids := "max"
	if l.PidsLimit > 0 {
		pids = strconv.FormatInt(l.PidsLimit, 10)
	}
	if err := writeFile(filepath.Join(c.dir("pids"), "pids.max"), pids); err != nil && l.PidsLimit > 0 {
		return fmt.Errorf("limite de processos: %w", err)
	}
	return nil
}

func (c *v1) AddProc(pid int) error {
	for _, ctrl := range v1Controllers {
		if err := writeFile(filepath.Join(c.dir(ctrl), "cgroup.procs"), strconv.Itoa(pid)); err != nil {
			return fmt.Errorf("cgroup v1 %s: %w", ctrl, err)
		}
	}
	return nil
}

func (c *v1) Procs() ([]int, error) { return readProcs(filepath.Join(c.dir("memory"), "cgroup.procs")) }

func (c *v1) Stats() (Stats, error) {
	if _, err := os.Stat(c.dir("memory")); err != nil {
		return Stats{}, err
	}
	var s Stats
	if ns, err := readUint(filepath.Join(c.dir("cpuacct"), "cpuacct.usage")); err == nil {
		s.CPUUsageUsec = ns / 1000
	}
	s.CPUThrottled = readKV(filepath.Join(c.dir("cpu"), "cpu.stat"))["throttled_time"] / 1000
	mem := c.dir("memory")
	s.MemoryUsage, _ = readUint(filepath.Join(mem, "memory.usage_in_bytes"))
	if lim, err := readUint(filepath.Join(mem, "memory.limit_in_bytes")); err == nil && lim < 1<<62 {
		s.MemoryLimit = lim
	}
	s.MemoryPeak, _ = readUint(filepath.Join(mem, "memory.max_usage_in_bytes"))
	s.OOMKills = readKV(filepath.Join(mem, "memory.oom_control"))["oom_kill"]
	s.PidsCurrent, _ = readUint(filepath.Join(c.dir("pids"), "pids.current"))
	s.PidsLimit, _ = readUint(filepath.Join(c.dir("pids"), "pids.max"))
	return s, nil
}

func (c *v1) Destroy() error {
	var first error
	for _, ctrl := range v1Controllers {
		if err := rmdirRetry(c.dir(ctrl)); err != nil && first == nil {
			first = err
		}
	}
	return first
}
