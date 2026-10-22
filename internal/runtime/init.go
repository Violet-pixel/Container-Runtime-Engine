package runtime

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/Violet-pixel/Container-Runtime-Engine/internal/store"
)

type InitConfig struct {
	Storage    string            `json:"storage"`
	Lower      string            `json:"lower"`
	Upper      string            `json:"upper"`
	Work       string            `json:"work"`
	Rootfs     string            `json:"rootfs"`
	Hostname   string            `json:"hostname"`
	Args       []string          `json:"args"`
	Env        []string          `json:"env"`
	Cwd        string            `json:"cwd"`
	User       string            `json:"user"`
	Mounts     []store.Mount     `json:"mounts"`
	EtcFiles   map[string]string `json:"etcFiles"`
	ReadOnly   bool              `json:"readOnly"`
	Network    string            `json:"network"`
	Privileged bool              `json:"privileged"`
}

const (
	fdPayload = 3
	fdSync    = 4
	fdError   = 5
)

func RunInit() {
	errPipe := os.NewFile(fdError, "error")
	fail := func(err error) {
		fmt.Fprint(errPipe, err.Error())
		os.Exit(127)
	}
	payload := os.NewFile(fdPayload, "payload")
	var cfg InitConfig
	if err := json.NewDecoder(payload).Decode(&cfg); err != nil {
		fail(fmt.Errorf("configuração do init: %w", err))
	}
	payload.Close()
	sync := os.NewFile(fdSync, "sync")
	buf := make([]byte, 1)
	if _, err := io.ReadFull(sync, buf); err != nil {
		os.Exit(1)
	}
	sync.Close()
	if err := setupContainer(&cfg); err != nil {
		fail(err)
	}
	path, err := lookPath(cfg.Args[0], cfg.Env)
	if err != nil {
		fail(err)
	}
	syscall.CloseOnExec(fdError)
	if err := syscall.Exec(path, cfg.Args, cfg.Env); err != nil {
		fail(fmt.Errorf("exec %s: %w", cfg.Args[0], err))
	}
}

func lookPath(name string, env []string) (string, error) {
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			os.Setenv("PATH", e[5:])
		}
	}
	if os.Getenv("PATH") == "" {
		os.Setenv("PATH", defaultPath)
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("comando %q não encontrado no container", name)
	}
	return p, nil
}

func mount(source, target, fstype string, flags uintptr, data string) error {
	if err := syscall.Mount(source, target, fstype, flags, data); err != nil {
		return fmt.Errorf("mount %s em %s: %w", fstype+source, target, err)
	}
	return nil
}

func ensureFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

func inside(root, p string) string {
	return filepath.Join(root, filepath.Clean("/"+p))
}

func setupContainer(cfg *InitConfig) error {
	_ = syscall.Unshare(syscall.CLONE_NEWCGROUP)
	if err := mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return err
	}
	root := cfg.Rootfs
	switch cfg.Storage {
	case "overlay":
		opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", cfg.Lower, cfg.Upper, cfg.Work)
		if err := mount("overlay", root, "overlay", 0, opts); err != nil {
			return fmt.Errorf("%w (defina SILO_STORAGE=copy se o kernel não suportar overlayfs)", err)
		}
	default:
		if err := mount(root, root, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
			return err
		}
	}
	for _, m := range cfg.Mounts {
		target := inside(root, m.Target)
		fi, err := os.Stat(m.Source)
		if err != nil {
			return fmt.Errorf("volume %s: %w", m.Source, err)
		}
		if fi.IsDir() {
			os.MkdirAll(target, 0o755)
		} else if err := ensureFile(target); err != nil {
			return err
		}
		if err := mount(m.Source, target, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
			return err
		}
		if m.ReadOnly {
			if err := mount("", target, "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY|syscall.MS_REC, ""); err != nil {
				return err
			}
		}
	}
	os.MkdirAll(inside(root, "/proc"), 0o555)
	if err := mount("proc", inside(root, "/proc"), "proc", syscall.MS_NOSUID|syscall.MS_NOEXEC|syscall.MS_NODEV, ""); err != nil {
		return err
	}
	os.MkdirAll(inside(root, "/sys"), 0o555)
	if cfg.Network == "host" {
		if mount("/sys", inside(root, "/sys"), "", syscall.MS_BIND|syscall.MS_REC, "") == nil {
			syscall.Mount("", inside(root, "/sys"), "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY|syscall.MS_REC, "")
		}
	} else {
		_ = syscall.Mount("sysfs", inside(root, "/sys"), "sysfs", syscall.MS_RDONLY|syscall.MS_NOSUID|syscall.MS_NOEXEC|syscall.MS_NODEV, "")
	}
	if err := setupDev(root); err != nil {
		return err
	}
	if _, err := os.Stat(inside(root, "/etc")); err == nil {
		for target, src := range cfg.EtcFiles {
			p := inside(root, target)
			if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				os.Remove(p)
			}
			if ensureFile(p) == nil {
				_ = syscall.Mount(src, p, "", syscall.MS_BIND, "")
			}
		}
	}
	if err := pivot(root); err != nil {
		return err
	}
	maskPaths()
	if cfg.ReadOnly {
		if err := mount("", "/", "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY, ""); err != nil {
			return err
		}
	}
	if err := syscall.Sethostname([]byte(cfg.Hostname)); err != nil {
		return fmt.Errorf("hostname: %w", err)
	}
	cwd := cfg.Cwd
	if cwd == "" {
		cwd = "/"
	}
	os.MkdirAll(cwd, 0o755)
	if err := syscall.Chdir(cwd); err != nil {
		return fmt.Errorf("diretório de trabalho %s: %w", cwd, err)
	}
	if !cfg.Privileged {
		dropCapabilities()
	}
	if cfg.User != "" && cfg.User != "root" && cfg.User != "0" {
		if err := switchUser(cfg.User); err != nil {
			return err
		}
	}
	return nil
}

func setupDev(root string) error {
	dev := inside(root, "/dev")
	os.MkdirAll(dev, 0o755)
	if err := mount("tmpfs", dev, "tmpfs", syscall.MS_NOSUID|syscall.MS_STRICTATIME, "mode=755,size=65536k"); err != nil {
		return err
	}
	for _, d := range []string{"null", "zero", "full", "random", "urandom", "tty"} {
		target := filepath.Join(dev, d)
		if err := ensureFile(target); err != nil {
			return err
		}
		if err := mount("/dev/"+d, target, "", syscall.MS_BIND, ""); err != nil {
			return err
		}
	}
	links := map[string]string{
		"fd": "/proc/self/fd", "stdin": "/proc/self/fd/0", "stdout": "/proc/self/fd/1",
		"stderr": "/proc/self/fd/2", "ptmx": "pts/ptmx", "core": "/proc/kcore",
	}
	for name, target := range links {
		os.Symlink(target, filepath.Join(dev, name))
	}
	os.MkdirAll(filepath.Join(dev, "pts"), 0o755)
	_ = syscall.Mount("devpts", filepath.Join(dev, "pts"), "devpts", syscall.MS_NOSUID|syscall.MS_NOEXEC, "newinstance,ptmxmode=0666,mode=0620")
	os.MkdirAll(filepath.Join(dev, "shm"), 0o1777)
	_ = syscall.Mount("shm", filepath.Join(dev, "shm"), "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, "mode=1777,size=65536k")
	os.MkdirAll(filepath.Join(dev, "mqueue"), 0o755)
	return nil
}

func pivot(root string) error {
	old := filepath.Join(root, ".silo-old-root")
	if err := os.MkdirAll(old, 0o700); err != nil {
		return err
	}
	if err := syscall.PivotRoot(root, old); err != nil {
		os.Remove(old)
		if err := syscall.Chroot(root); err != nil {
			return fmt.Errorf("pivot_root e chroot falharam: %w", err)
		}
		return syscall.Chdir("/")
	}
	if err := syscall.Chdir("/"); err != nil {
		return err
	}
	if err := syscall.Unmount("/.silo-old-root", syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("desmontar raiz antiga: %w", err)
	}
	return os.Remove("/.silo-old-root")
}

func maskPaths() {
	for _, p := range []string{"/proc/kcore", "/proc/keys", "/proc/timer_list", "/proc/sched_debug", "/proc/acpi", "/sys/firmware"} {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if fi.IsDir() {
			syscall.Mount("tmpfs", p, "tmpfs", syscall.MS_RDONLY, "size=0")
		} else {
			syscall.Mount("/dev/null", p, "", syscall.MS_BIND, "")
		}
	}
	for _, p := range []string{"/proc/sys", "/proc/sysrq-trigger", "/proc/irq", "/proc/bus"} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if syscall.Mount(p, p, "", syscall.MS_BIND|syscall.MS_REC, "") == nil {
			syscall.Mount("", p, "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY|syscall.MS_REC, "")
		}
	}
}

var keepCaps = map[int]bool{
	0: true, 1: true, 3: true, 4: true, 5: true, 6: true, 7: true, 8: true,
	10: true, 13: true, 18: true, 27: true, 29: true, 31: true,
}

const prCapbsetDrop = 24

func dropCapabilities() {
	last := 40
	if b, err := os.ReadFile("/proc/sys/kernel/cap_last_cap"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			last = n
		}
	}
	for c := 0; c <= last; c++ {
		if !keepCaps[c] {
			syscall.RawSyscall(syscall.SYS_PRCTL, prCapbsetDrop, uintptr(c), 0)
		}
	}
}

func lookupID(file, name string) (int, int, bool) {
	f, err := os.Open(file)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), ":")
		if len(parts) >= 4 && parts[0] == name {
			a, _ := strconv.Atoi(parts[2])
			b, _ := strconv.Atoi(parts[3])
			return a, b, true
		}
		if len(parts) >= 3 && parts[0] == name {
			a, _ := strconv.Atoi(parts[2])
			return a, a, true
		}
	}
	return 0, 0, false
}

func switchUser(spec string) error {
	userPart, groupPart, _ := strings.Cut(spec, ":")
	uid, gid := -1, -1
	if n, err := strconv.Atoi(userPart); err == nil {
		uid, gid = n, n
		if u, g, ok := lookupUID(n); ok {
			uid, gid = u, g
		}
	} else if u, g, ok := lookupID("/etc/passwd", userPart); ok {
		uid, gid = u, g
	} else {
		return fmt.Errorf("usuário %q não existe na imagem", userPart)
	}
	if groupPart != "" {
		if n, err := strconv.Atoi(groupPart); err == nil {
			gid = n
		} else if g, _, ok := lookupID("/etc/group", groupPart); ok {
			gid = g
		} else {
			return fmt.Errorf("grupo %q não existe na imagem", groupPart)
		}
	}
	if err := syscall.Setgroups([]int{}); err != nil {
		return err
	}
	if err := syscall.Setgid(gid); err != nil {
		return fmt.Errorf("setgid: %w", err)
	}
	if err := syscall.Setuid(uid); err != nil {
		return fmt.Errorf("setuid: %w", err)
	}
	return nil
}

func lookupUID(uid int) (int, int, bool) {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), ":")
		if len(parts) >= 4 && parts[2] == strconv.Itoa(uid) {
			g, _ := strconv.Atoi(parts[3])
			return uid, g, true
		}
	}
	return 0, 0, false
}
