package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Violet-pixel/Container-Runtime-Engine/internal/cgroups"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/network"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/store"
)

type Stdio struct {
	In         io.Reader
	Out        io.Writer
	Err        io.Writer
	Foreground bool
}

func (r *Runtime) Supervise(id string, stdio *Stdio) (int, error) {
	goruntime.LockOSThread()
	c, err := r.Store.GetContainer(id)
	if err != nil {
		return 125, err
	}
	if stdio != nil && stdio.Foreground {
		if c.State == store.StateRunning {
			return 125, ErrAlreadyRunning
		}
		if err := network.Requirements(c.Spec.Network); err != nil {
			return 125, err
		}
		if err := r.checkPorts(c); err != nil {
			return 125, err
		}
	}
	code := 0
	for {
		var runErr error
		code, runErr = r.runOnce(c, stdio)
		cur, err := r.Store.GetContainer(id)
		if err != nil {
			return code, runErr
		}
		c = cur
		if runErr != nil {
			return code, runErr
		}
		if c.StopRequested || !shouldRestart(c, code) {
			break
		}
		c.RestartCount++
		r.Store.UpdateContainer(c)
		r.event("restart", c, map[string]string{"attempt": strconv.Itoa(c.RestartCount)})
		backoff := time.Duration(100<<min(c.RestartCount, 7)) * time.Millisecond
		time.Sleep(backoff)
		cur, err = r.Store.GetContainer(id)
		if err != nil || cur.StopRequested {
			break
		}
		c = cur
	}
	if c.Spec.AutoRemove {
		r.destroy(c)
	}
	return code, nil
}

func shouldRestart(c *store.Container, code int) bool {
	switch c.Spec.Restart.Name {
	case "always":
		return c.Spec.Restart.MaxRetries == 0 || c.RestartCount < c.Spec.Restart.MaxRetries
	case "on-failure":
		if code == 0 {
			return false
		}
		return c.Spec.Restart.MaxRetries == 0 || c.RestartCount < c.Spec.Restart.MaxRetries
	}
	return false
}

func (r *Runtime) fail(c *store.Container, msg string) {
	now := time.Now()
	c.State = store.StateExited
	c.Error = msg
	c.ExitCode = 125
	c.Pid = 0
	c.ShimPid = 0
	c.FinishedAt = &now
	r.Store.UpdateContainer(c)
	r.event("error", c, map[string]string{"error": msg})
}

func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

func (r *Runtime) writeEtc(c *store.Container, ip string) (map[string]string, error) {
	dir := r.Cfg.ContainerDir(c.ID)
	hosts := "127.0.0.1\tlocalhost\n::1\tlocalhost ip6-localhost ip6-loopback\n"
	if ip != "" {
		hosts += fmt.Sprintf("%s\t%s %s\n", ip, c.Spec.Hostname, c.Name)
	} else {
		hosts += fmt.Sprintf("127.0.1.1\t%s %s\n", c.Spec.Hostname, c.Name)
	}
	files := map[string][]byte{
		"hostname":    []byte(c.Spec.Hostname + "\n"),
		"hosts":       []byte(hosts),
		"resolv.conf": network.ResolvConf(c.Spec.DNS, r.Cfg.DefaultDNS),
	}
	out := map[string]string{}
	for name, data := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return nil, err
		}
		out["/etc/"+name] = p
	}
	return out, nil
}

func (r *Runtime) runOnce(c *store.Container, stdio *Stdio) (int, error) {
	img, err := r.Store.GetImage(c.ImageID)
	if err != nil {
		r.fail(c, "imagem do container não existe mais")
		return 125, err
	}
	dir := r.Cfg.ContainerDir(c.ID)
	cg := r.cgroup(c.ID)
	cg.Destroy()
	limits := cgroups.Limits{CPUs: c.Spec.Resources.CPUs, CPUWeight: c.Spec.Resources.CPUWeight,
		MemoryMB: c.Spec.Resources.MemoryMB, PidsLimit: c.Spec.Resources.PidsLimit}
	if err := cg.Create(limits); err != nil {
		r.fail(c, err.Error())
		return 125, err
	}

	var ip net.IP
	var unlock func()
	if c.Spec.Network == network.ModeBridge {
		unlock, err = lockFile(filepath.Join(r.Cfg.Root, "network.lock"))
		if err != nil {
			r.fail(c, err.Error())
			return 125, err
		}
		if err := r.Bridge.Ensure(); err != nil {
			unlock()
			r.fail(c, "bridge: "+err.Error())
			return 125, err
		}
		used, _ := r.Store.UsedIPs()
		ip, err = r.Bridge.Allocate(used)
		if err != nil {
			unlock()
			r.fail(c, err.Error())
			return 125, err
		}
	}
	release := func() {
		if unlock != nil {
			unlock()
			unlock = nil
		}
	}
	defer release()

	ipStr := ""
	if ip != nil {
		ipStr = ip.String()
	}
	etc, err := r.writeEtc(c, ipStr)
	if err != nil {
		r.fail(c, err.Error())
		return 125, err
	}
	env := mergeEnv([]string{"PATH=" + defaultPath}, img.Config.Env)
	env = mergeEnv(env, []string{"HOSTNAME=" + c.Spec.Hostname, "container=silo"})
	env = mergeEnv(env, c.Spec.Env)
	if stdio != nil && stdio.Foreground && isTerminal(os.Stdin) {
		env = mergeEnv([]string{"TERM=xterm"}, env)
	}
	initCfg := InitConfig{
		Storage: c.Storage, Rootfs: filepath.Join(dir, "rootfs"), Hostname: c.Spec.Hostname,
		Args: c.Command, Env: env, Cwd: c.Spec.WorkingDir, User: c.Spec.User, Mounts: c.Spec.Mounts,
		EtcFiles: etc, ReadOnly: c.Spec.ReadOnlyRootfs, Network: c.Spec.Network, Privileged: c.Spec.Privileged,
	}
	if c.Storage == "overlay" {
		initCfg.Lower = img.Rootfs
		initCfg.Upper = filepath.Join(dir, "upper")
		initCfg.Work = filepath.Join(dir, "work")
	}

	payR, payW, _ := os.Pipe()
	syncR, syncW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	cmd := exec.Command(r.exe, "init")
	cmd.Env = []string{}
	cmd.ExtraFiles = []*os.File{payR, syncR, errW}
	flags := syscall.CLONE_NEWUTS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS | syscall.CLONE_NEWIPC
	if c.Spec.Network != network.ModeHost {
		flags |= syscall.CLONE_NEWNET
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(flags), Pdeathsig: syscall.SIGKILL}

	var logf *os.File
	if stdio != nil && stdio.Foreground {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = stdio.In, stdio.Out, stdio.Err
	} else {
		logf, err = os.OpenFile(r.LogPath(c.ID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
		if err != nil {
			r.fail(c, err.Error())
			return 125, err
		}
		defer logf.Close()
		devnull, _ := os.Open(os.DevNull)
		defer devnull.Close()
		cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, logf, logf
	}
	if err := cmd.Start(); err != nil {
		payR.Close()
		payW.Close()
		syncR.Close()
		syncW.Close()
		errR.Close()
		errW.Close()
		r.fail(c, "iniciar processo: "+err.Error())
		return 125, err
	}
	payR.Close()
	syncR.Close()
	errW.Close()
	pid := cmd.Process.Pid
	abort := func(msg string) (int, error) {
		syscall.Kill(pid, syscall.SIGKILL)
		syncW.Close()
		cmd.Wait()
		errR.Close()
		network.Detach(c.ID)
		cg.Destroy()
		r.fail(c, msg)
		return 125, errors.New(msg)
	}
	if err := json.NewEncoder(payW).Encode(initCfg); err != nil {
		payW.Close()
		return abort("enviar configuração: " + err.Error())
	}
	payW.Close()
	if err := cg.AddProc(pid); err != nil {
		return abort("cgroup: " + err.Error())
	}
	switch c.Spec.Network {
	case network.ModeBridge:
		if err := r.Bridge.Attach(pid, c.ID, ip); err != nil {
			return abort("rede: " + err.Error())
		}
	case network.ModeNone:
		if err := network.LoopbackOnly(pid); err != nil {
			return abort("rede: " + err.Error())
		}
	}
	if _, err := syncW.Write([]byte{1}); err != nil {
		return abort("sincronizar init: " + err.Error())
	}
	syncW.Close()
	msg, _ := io.ReadAll(errR)
	errR.Close()
	if len(msg) > 0 {
		cmd.Wait()
		network.Detach(c.ID)
		cg.Destroy()
		r.fail(c, strings.TrimSpace(string(msg)))
		return 127, errors.New(strings.TrimSpace(string(msg)))
	}

	now := time.Now()
	c.State = store.StateRunning
	c.Pid = pid
	c.PidStart = procStart(pid)
	c.ShimPid = os.Getpid()
	c.IP = ipStr
	c.Error = ""
	c.ExitCode = 0
	c.StartedAt = &now
	c.FinishedAt = nil
	if err := r.Store.UpdateContainer(c); err != nil {
		return abort(err.Error())
	}
	release()

	var proxies []*network.Proxy
	for _, p := range c.Spec.Ports {
		px, err := network.StartProxy(p.HostIP, p.HostPort, net.JoinHostPort(ipStr, strconv.Itoa(p.ContainerPort)), p.Protocol)
		if err != nil {
			if logf != nil {
				fmt.Fprintf(logf, "silo: porta %d/%s indisponível: %v\n", p.HostPort, p.Protocol, err)
			}
			r.Store.AddEvent(store.Event{Type: "container", Action: "port-error", ActorID: c.ID, ActorName: c.Name,
				Attributes: map[string]string{"port": strconv.Itoa(p.HostPort), "error": err.Error()}})
			continue
		}
		proxies = append(proxies, px)
	}
	r.event("start", c, map[string]string{"pid": strconv.Itoa(pid), "ip": ipStr})

	sigs := make(chan os.Signal, 4)
	stopFwd := make(chan struct{})
	if stdio != nil && stdio.Foreground {
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		go func() {
			count := 0
			for {
				select {
				case <-stopFwd:
					return
				case <-sigs:
					count++
					r.Store.SetStopRequested(c.ID, true)
					if count == 1 {
						syscall.Kill(pid, syscall.SIGTERM)
						go func() {
							time.Sleep(time.Duration(r.Cfg.StopTimeout) * time.Second)
							syscall.Kill(pid, syscall.SIGKILL)
						}()
					} else {
						syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			}
		}()
	} else {
		signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
		go func() {
			select {
			case <-stopFwd:
			case <-sigs:
				r.Store.SetStopRequested(c.ID, true)
				syscall.Kill(pid, syscall.SIGTERM)
			}
		}()
	}

	waitErr := cmd.Wait()
	close(stopFwd)
	signal.Stop(sigs)
	code := exitCode(cmd, waitErr)
	stats, _ := cg.Stats()
	for _, p := range proxies {
		p.Close()
	}
	network.Detach(c.ID)
	cg.Destroy()

	cur, err := r.Store.GetContainer(c.ID)
	if err == nil {
		c = cur
	}
	fin := time.Now()
	c.State = store.StateExited
	c.ExitCode = code
	c.Pid = 0
	c.PidStart = 0
	c.ShimPid = 0
	c.FinishedAt = &fin
	attrs := map[string]string{"exitCode": strconv.Itoa(code)}
	if stats.OOMKills > 0 {
		c.Error = "o processo foi encerrado por falta de memória (OOM)"
		attrs["oom"] = "true"
		r.event("oom", c, nil)
	}
	r.Store.UpdateContainer(c)
	r.event("die", c, attrs)
	return code, nil
}

func exitCode(cmd *exec.Cmd, err error) int {
	if cmd.ProcessState == nil {
		return 125
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
		if ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ws.ExitStatus()
	}
	if err != nil {
		return 1
	}
	return 0
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
