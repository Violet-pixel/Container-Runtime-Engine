package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/Violet-pixel/Container-Runtime-Engine/internal/api"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/config"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/runtime"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/store"
)

type command struct {
	name  string
	args  string
	short string
	run   func(rt *runtime.Runtime, args []string) error
}

var commands []command

func init() {
	commands = []command{
		{"run", "[opções] IMAGEM [COMANDO...]", "Cria e inicia um container", cmdRun},
		{"create", "[opções] IMAGEM [COMANDO...]", "Cria um container sem iniciar", cmdCreate},
		{"start", "[-a] CONTAINER...", "Inicia containers parados", cmdStart},
		{"stop", "[-t segundos] CONTAINER...", "Para containers (SIGTERM e depois SIGKILL)", cmdStop},
		{"restart", "[-t segundos] CONTAINER...", "Reinicia containers", cmdRestart},
		{"kill", "[-s SINAL] CONTAINER...", "Envia um sinal ao processo principal", cmdKill},
		{"rm", "[-f] CONTAINER...", "Remove containers", cmdRm},
		{"ps", "[-a] [-q]", "Lista containers", cmdPs},
		{"inspect", "CONTAINER|IMAGEM", "Mostra os detalhes em JSON", cmdInspect},
		{"logs", "[-f] [-n linhas] CONTAINER", "Mostra a saída do container", cmdLogs},
		{"exec", "[-e CHAVE=valor] [-w dir] [-u uid] CONTAINER COMANDO...", "Executa um comando dentro do container", cmdExec},
		{"stats", "[--no-stream] [CONTAINER...]", "Uso de CPU, memória, processos e rede", cmdStats},
		{"top", "CONTAINER", "Processos em execução no container", cmdTop},
		{"pull", "IMAGEM", "Baixa uma imagem de um registro OCI/Docker", cmdPull},
		{"import", "ARQUIVO.tar[.gz] NOME[:tag] [COMANDO...]", "Importa um rootfs em tar como imagem", cmdImport},
		{"images", "[-q]", "Lista imagens", cmdImages},
		{"rmi", "IMAGEM...", "Remove imagens", cmdRmi},
		{"prune", "", "Remove todos os containers parados", cmdPrune},
		{"events", "[-f] [-n quantidade]", "Mostra o histórico de eventos", cmdEvents},
		{"info", "", "Informações do host e do runtime", cmdInfo},
		{"serve", "[--addr host:porta] [--token TOKEN]", "Sobe a API REST e a interface web", cmdServe},
		{"version", "", "Versão do Silo", nil},
	}
}

func usage() {
	fmt.Printf("Silo %s, runtime de containers com namespaces e cgroups\n\nUso: silo COMANDO [opções]\n\nComandos:\n", config.Version)
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, c := range commands {
		fmt.Fprintf(w, "  %s\t%s\n", c.name, c.short)
	}
	w.Flush()
	fmt.Println("\nUse \"silo COMANDO --help\" para ver as opções de cada comando.")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "init":
		runtime.RunInit()
		os.Exit(127)
	case "version", "--version", "-v":
		fmt.Println("silo", config.Version)
		return
	case "help", "--help", "-h":
		usage()
		return
	}
	if os.Geteuid() != 0 {
		fail(errors.New("o Silo precisa rodar como root (use sudo), porque cria namespaces, cgroups e interfaces de rede"))
	}
	cfg := config.Load()
	if err := cfg.Ensure(); err != nil {
		fail(err)
	}
	st, err := store.Open(cfg.DBPath(), cfg.EventsRetain)
	if err != nil {
		fail(fmt.Errorf("banco de dados: %w", err))
	}
	defer st.Close()
	rt, err := runtime.New(cfg, st)
	if err != nil {
		fail(err)
	}
	switch os.Args[1] {
	case "shim":
		if len(os.Args) < 3 {
			os.Exit(2)
		}
		code, err := rt.Supervise(os.Args[2], nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, "silo shim:", err)
		}
		st.Close()
		os.Exit(code)
	case "__enter":
		if len(os.Args) < 4 {
			os.Exit(2)
		}
		if err := rt.Enter(os.Args[2], os.Args[3:]); err != nil {
			fail(err)
		}
		return
	}
	for _, c := range commands {
		if c.name == os.Args[1] && c.run != nil {
			if err := c.run(rt, os.Args[2:]); err != nil {
				st.Close()
				var ec exitCodeErr
				if errors.As(err, &ec) {
					os.Exit(int(ec))
				}
				fail(err)
			}
			return
		}
	}
	fmt.Fprintf(os.Stderr, "comando desconhecido: %s\n\n", os.Args[1])
	usage()
	os.Exit(1)
}

type exitCodeErr int

func (e exitCodeErr) Error() string { return "código de saída " + strconv.Itoa(int(e)) }

func fail(err error) {
	fmt.Fprintln(os.Stderr, "erro:", err)
	os.Exit(1)
}

func newFlags(name, args string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Uso: silo %s %s\n\nOpções:\n", name, args)
		fs.PrintDefaults()
	}
	return fs
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

type specFlags struct {
	name, hostname, workdir, user, network, memory, restart, entrypoint string
	cpus                                                                float64
	cpuWeight                                                           int
	pids                                                                int64
	readOnly, rm, privileged, detach                                    bool
	env, ports, volumes, dns, labels                                    multi
}

func bindSpec(fs *flag.FlagSet, f *specFlags) {
	fs.StringVar(&f.name, "name", "", "nome do container")
	fs.StringVar(&f.hostname, "hostname", "", "hostname dentro do container")
	fs.StringVar(&f.workdir, "w", "", "diretório de trabalho")
	fs.StringVar(&f.user, "u", "", "usuário (nome ou uid[:gid])")
	fs.StringVar(&f.network, "network", "bridge", "rede: bridge, host ou none")
	fs.StringVar(&f.memory, "m", "", "limite de memória (ex.: 256m, 1g)")
	fs.Float64Var(&f.cpus, "cpus", 0, "limite de CPU em núcleos (ex.: 0.5)")
	fs.IntVar(&f.cpuWeight, "cpu-weight", 0, "peso relativo de CPU (1 a 10000)")
	fs.Int64Var(&f.pids, "pids-limit", 0, "máximo de processos")
	fs.StringVar(&f.restart, "restart", "no", "política de reinício: no, on-failure[:máx] ou always")
	fs.StringVar(&f.entrypoint, "entrypoint", "", "substitui o entrypoint da imagem")
	fs.BoolVar(&f.readOnly, "read-only", false, "monta o sistema de arquivos raiz como somente leitura")
	fs.BoolVar(&f.rm, "rm", false, "remove o container quando ele terminar")
	fs.BoolVar(&f.privileged, "privileged", false, "mantém todas as capabilities do root")
	fs.Var(&f.env, "e", "variável de ambiente CHAVE=valor (repetível)")
	fs.Var(&f.ports, "p", "porta [ip:]host:container[/tcp|udp] (repetível)")
	fs.Var(&f.volumes, "v", "volume /origem:/destino[:ro] (repetível)")
	fs.Var(&f.dns, "dns", "servidor DNS (repetível)")
	fs.Var(&f.labels, "label", "rótulo chave=valor (repetível)")
}

func buildSpec(f *specFlags, rest []string) (store.Spec, error) {
	if len(rest) == 0 {
		return store.Spec{}, errors.New("informe a imagem")
	}
	spec := store.Spec{
		Image: rest[0], Cmd: rest[1:], Env: f.env, WorkingDir: f.workdir, Hostname: f.hostname, User: f.user,
		Network: f.network, ReadOnlyRootfs: f.readOnly, AutoRemove: f.rm, Privileged: f.privileged, DNS: f.dns,
	}
	if f.entrypoint != "" {
		spec.Entrypoint = strings.Fields(f.entrypoint)
	}
	mem, err := runtime.ParseMemory(f.memory)
	if err != nil {
		return spec, err
	}
	spec.Resources = store.Resources{CPUs: f.cpus, CPUWeight: f.cpuWeight, MemoryMB: mem, PidsLimit: f.pids}
	if spec.Restart, err = runtime.ParseRestart(f.restart); err != nil {
		return spec, err
	}
	for _, p := range f.ports {
		pm, err := runtime.ParsePort(p)
		if err != nil {
			return spec, err
		}
		spec.Ports = append(spec.Ports, pm)
	}
	for _, v := range f.volumes {
		m, err := runtime.ParseMount(v)
		if err != nil {
			return spec, err
		}
		spec.Mounts = append(spec.Mounts, m)
	}
	if len(f.labels) > 0 {
		spec.Labels = map[string]string{}
		for _, l := range f.labels {
			k, v, _ := strings.Cut(l, "=")
			spec.Labels[k] = v
		}
	}
	return spec, nil
}

func ensureImage(rt *runtime.Runtime, ref string) error {
	if _, err := rt.Images.Lookup(ref); err == nil {
		return nil
	}
	fmt.Fprintf(os.Stderr, "Imagem %s não encontrada localmente, baixando...\n", ref)
	_, err := rt.Images.Pull(ref, progressPrinter())
	fmt.Fprintln(os.Stderr)
	return err
}

func progressPrinter() func(string, int64, int64) {
	return func(stage string, cur, total int64) {
		if total > 0 {
			fmt.Fprintf(os.Stderr, "\r%-14s %6.1f / %6.1f MB", stage, float64(cur)/1e6, float64(total)/1e6)
		} else {
			fmt.Fprintf(os.Stderr, "\r%-14s", stage)
		}
	}
}

func cmdCreate(rt *runtime.Runtime, args []string) error {
	fs := newFlags("create", "[opções] IMAGEM [COMANDO...]")
	var f specFlags
	bindSpec(fs, &f)
	fs.Parse(args)
	spec, err := buildSpec(&f, fs.Args())
	if err != nil {
		return err
	}
	if err := ensureImage(rt, spec.Image); err != nil {
		return err
	}
	c, err := rt.Create(spec, f.name)
	if err != nil {
		return err
	}
	fmt.Println(c.ID)
	return nil
}

func cmdRun(rt *runtime.Runtime, args []string) error {
	fs := newFlags("run", "[opções] IMAGEM [COMANDO...]")
	var f specFlags
	bindSpec(fs, &f)
	fs.BoolVar(&f.detach, "d", false, "roda em segundo plano e mostra o ID")
	fs.Parse(args)
	spec, err := buildSpec(&f, fs.Args())
	if err != nil {
		return err
	}
	if err := ensureImage(rt, spec.Image); err != nil {
		return err
	}
	c, err := rt.Create(spec, f.name)
	if err != nil {
		return err
	}
	if f.detach {
		if _, err := rt.Start(c.ID); err != nil {
			return err
		}
		fmt.Println(c.ID)
		return nil
	}
	code, err := rt.Supervise(c.ID, &runtime.Stdio{In: os.Stdin, Out: os.Stdout, Err: os.Stderr, Foreground: true})
	if err != nil {
		return err
	}
	if code != 0 {
		return exitCodeErr(code)
	}
	return nil
}

func cmdStart(rt *runtime.Runtime, args []string) error {
	fs := newFlags("start", "[-a] CONTAINER...")
	attach := fs.Bool("a", false, "inicia em primeiro plano, ligado ao terminal (um container)")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return errors.New("informe ao menos um container")
	}
	if *attach {
		c, err := rt.Get(fs.Arg(0))
		if err != nil {
			return err
		}
		code, err := rt.Supervise(c.ID, &runtime.Stdio{In: os.Stdin, Out: os.Stdout, Err: os.Stderr, Foreground: true})
		if err != nil {
			return err
		}
		if code != 0 {
			return exitCodeErr(code)
		}
		return nil
	}
	return each(fs.Args(), func(ref string) error {
		c, err := rt.Start(ref)
		if err == nil {
			fmt.Println(c.Name)
		}
		return err
	})
}

func each(refs []string, fn func(string) error) error {
	failed := 0
	for _, ref := range refs {
		if err := fn(ref); err != nil {
			fmt.Fprintf(os.Stderr, "erro em %s: %v\n", ref, err)
			failed++
		}
	}
	if failed > 0 {
		return exitCodeErr(1)
	}
	return nil
}

func cmdStop(rt *runtime.Runtime, args []string) error {
	fs := newFlags("stop", "[-t segundos] CONTAINER...")
	t := fs.Int("t", 0, "segundos de espera antes do SIGKILL (padrão: SILO_STOP_TIMEOUT ou 10)")
	fs.Parse(args)
	return each(fs.Args(), func(ref string) error {
		c, err := rt.Stop(ref, time.Duration(*t)*time.Second)
		if err == nil {
			fmt.Println(c.Name)
		}
		return err
	})
}

func cmdRestart(rt *runtime.Runtime, args []string) error {
	fs := newFlags("restart", "[-t segundos] CONTAINER...")
	t := fs.Int("t", 0, "segundos de espera antes do SIGKILL")
	fs.Parse(args)
	return each(fs.Args(), func(ref string) error {
		c, err := rt.Restart(ref, time.Duration(*t)*time.Second)
		if err == nil {
			fmt.Println(c.Name)
		}
		return err
	})
}

var signals = map[string]syscall.Signal{
	"KILL": syscall.SIGKILL, "TERM": syscall.SIGTERM, "INT": syscall.SIGINT, "HUP": syscall.SIGHUP,
	"QUIT": syscall.SIGQUIT, "USR1": syscall.SIGUSR1, "USR2": syscall.SIGUSR2, "STOP": syscall.SIGSTOP, "CONT": syscall.SIGCONT,
}

func ParseSignal(s string) (syscall.Signal, error) {
	s = strings.TrimPrefix(strings.ToUpper(s), "SIG")
	if sig, ok := signals[s]; ok {
		return sig, nil
	}
	if n, err := strconv.Atoi(s); err == nil && n > 0 && n < 65 {
		return syscall.Signal(n), nil
	}
	return 0, fmt.Errorf("sinal desconhecido: %s", s)
}

func cmdKill(rt *runtime.Runtime, args []string) error {
	fs := newFlags("kill", "[-s SINAL] CONTAINER...")
	s := fs.String("s", "KILL", "sinal (nome ou número)")
	fs.Parse(args)
	sig, err := ParseSignal(*s)
	if err != nil {
		return err
	}
	return each(fs.Args(), func(ref string) error {
		c, err := rt.Kill(ref, sig)
		if err == nil {
			fmt.Println(c.Name)
		}
		return err
	})
}

func cmdRm(rt *runtime.Runtime, args []string) error {
	fs := newFlags("rm", "[-f] CONTAINER...")
	force := fs.Bool("f", false, "força a remoção de containers em execução")
	fs.Parse(args)
	return each(fs.Args(), func(ref string) error {
		c, err := rt.Remove(ref, *force)
		if err == nil {
			fmt.Println(c.Name)
		}
		return err
	})
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("há %ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("há %d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("há %d h", int(d.Hours()))
	}
	return fmt.Sprintf("há %d dias", int(d.Hours()/24))
}

func status(c store.Container) string {
	switch c.State {
	case store.StateRunning:
		if c.StartedAt != nil {
			return "Rodando " + ago(*c.StartedAt)
		}
		return "Rodando"
	case store.StateStopping:
		return "Parando"
	case store.StateExited:
		s := fmt.Sprintf("Saiu (%d)", c.ExitCode)
		if c.FinishedAt != nil {
			s += " " + ago(*c.FinishedAt)
		}
		return s
	}
	return "Criado"
}

func portsString(ps []store.PortMapping) string {
	var out []string
	for _, p := range ps {
		out = append(out, fmt.Sprintf("%s:%d->%d/%s", p.HostIP, p.HostPort, p.ContainerPort, p.Protocol))
	}
	return strings.Join(out, ", ")
}

func cmdPs(rt *runtime.Runtime, args []string) error {
	fs := newFlags("ps", "[-a] [-q]")
	all := fs.Bool("a", false, "inclui containers parados")
	quiet := fs.Bool("q", false, "mostra só os IDs")
	asJSON := fs.Bool("json", false, "saída em JSON")
	fs.Parse(args)
	list, err := rt.List(*all)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(list)
	}
	if *quiet {
		for _, c := range list {
			fmt.Println(short(c.ID))
		}
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 3, ' ', 0)
	fmt.Fprintln(w, "ID\tIMAGEM\tCOMANDO\tSTATUS\tIP\tPORTAS\tNOME")
	for _, c := range list {
		cmd := strings.Join(c.Command, " ")
		if len(cmd) > 28 {
			cmd = cmd[:27] + "…"
		}
		ip := c.IP
		if c.State != store.StateRunning {
			ip = ""
		}
		fmt.Fprintf(w, "%s\t%s\t%q\t%s\t%s\t%s\t%s\n", short(c.ID), c.ImageRef, cmd, status(c), ip, portsString(c.Spec.Ports), c.Name)
	}
	return w.Flush()
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func cmdInspect(rt *runtime.Runtime, args []string) error {
	if len(args) == 0 {
		return errors.New("informe um container ou uma imagem")
	}
	var out []any
	for _, ref := range args {
		if in, err := rt.Inspect(ref); err == nil {
			out = append(out, in)
			continue
		}
		img, err := rt.Images.Lookup(ref)
		if err != nil {
			return fmt.Errorf("%s: nenhum container ou imagem com esse nome", ref)
		}
		out = append(out, img)
	}
	return printJSON(out)
}

type flushWriter struct{ w *bufio.Writer }

func (f flushWriter) Write(p []byte) (int, error) { return f.w.Write(p) }
func (f flushWriter) Flush()                      { f.w.Flush() }

func cmdLogs(rt *runtime.Runtime, args []string) error {
	fs := newFlags("logs", "[-f] [-n linhas] CONTAINER")
	follow := fs.Bool("f", false, "acompanha a saída em tempo real")
	tail := fs.Int("n", 0, "mostra só as últimas N linhas")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("informe um container")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	bw := bufio.NewWriter(os.Stdout)
	defer bw.Flush()
	return rt.Logs(ctx, fs.Arg(0), *tail, *follow, flushWriter{bw})
}

func cmdExec(rt *runtime.Runtime, args []string) error {
	fs := newFlags("exec", "[opções] CONTAINER COMANDO...")
	var env multi
	fs.Var(&env, "e", "variável de ambiente CHAVE=valor (repetível)")
	wd := fs.String("w", "", "diretório de trabalho")
	user := fs.String("u", "", "uid[:gid] numérico")
	fs.Bool("it", false, "compatibilidade: o terminal é sempre repassado")
	fs.Bool("i", false, "compatibilidade")
	fs.Bool("t", false, "compatibilidade")
	fs.Parse(args)
	if fs.NArg() < 2 {
		return errors.New("informe o container e o comando")
	}
	signal.Ignore(syscall.SIGINT)
	code, err := rt.Exec(fs.Arg(0), runtime.ExecOptions{
		Cmd: fs.Args()[1:], Env: env, WorkDir: *wd, User: *user, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
	})
	if err != nil {
		return err
	}
	if code != 0 {
		return exitCodeErr(code)
	}
	return nil
}

func human(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func cmdStats(rt *runtime.Runtime, args []string) error {
	fs := newFlags("stats", "[--no-stream] [CONTAINER...]")
	noStream := fs.Bool("no-stream", false, "mostra uma leitura e sai")
	fs.Parse(args)
	refs := fs.Args()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	for first := true; ; first = false {
		targets := refs
		if len(targets) == 0 {
			list, err := rt.List(false)
			if err != nil {
				return err
			}
			for _, c := range list {
				targets = append(targets, c.ID)
			}
		}
		if first {
			for _, t := range targets {
				rt.Stats(t)
			}
			time.Sleep(time.Second)
		}
		var rows []*runtime.Stats
		for _, t := range targets {
			if s, err := rt.Stats(t); err == nil {
				rows = append(rows, s)
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		if !*noStream {
			fmt.Print("\033[H\033[2J")
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 3, ' ', 0)
		fmt.Fprintln(w, "ID\tNOME\tCPU %\tMEMÓRIA / LIMITE\tMEM %\tPROCESSOS\tREDE RX / TX")
		for _, s := range rows {
			limit := "sem limite"
			if s.Cgroup.MemoryLimit > 0 {
				limit = human(s.Cgroup.MemoryLimit)
			}
			pids := strconv.FormatUint(s.Cgroup.PidsCurrent, 10)
			if s.Cgroup.PidsLimit > 0 {
				pids += " / " + strconv.FormatUint(s.Cgroup.PidsLimit, 10)
			}
			fmt.Fprintf(w, "%s\t%s\t%.2f%%\t%s / %s\t%.2f%%\t%s\t%s / %s\n", short(s.ID), s.Name, s.CPUPercent,
				human(s.Cgroup.MemoryUsage), limit, s.MemPercent, pids, human(s.Network.RxBytes), human(s.Network.TxBytes))
		}
		w.Flush()
		if *noStream {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
}

func cmdTop(rt *runtime.Runtime, args []string) error {
	if len(args) != 1 {
		return errors.New("informe um container")
	}
	procs, err := rt.Top(args[0])
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 3, ' ', 0)
	fmt.Fprintln(w, "PID\tPPID\tUID\tESTADO\tRSS\tTHREADS\tCOMANDO")
	for _, p := range procs {
		fmt.Fprintf(w, "%d\t%d\t%s\t%s\t%s\t%d\t%s\n", p.Pid, p.PPid, p.UID, p.State, human(uint64(p.RSSKB)*1024), p.Threads, p.Command)
	}
	return w.Flush()
}

func cmdPull(rt *runtime.Runtime, args []string) error {
	if len(args) != 1 {
		return errors.New("informe a imagem (ex.: alpine:3.20)")
	}
	img, err := rt.Images.Pull(args[0], progressPrinter())
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	fmt.Printf("%s %s\n", img.Reference, img.Digest)
	return nil
}

func cmdImport(rt *runtime.Runtime, args []string) error {
	if len(args) < 2 {
		return errors.New("uso: silo import ARQUIVO.tar[.gz] NOME[:tag] [COMANDO...]")
	}
	var r io.Reader = os.Stdin
	if args[0] != "-" {
		f, err := os.Open(args[0])
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	img, err := rt.Images.Import(r, args[1], args[2:])
	if err != nil {
		return err
	}
	fmt.Printf("%s %s\n", img.Reference, short(img.ID))
	return nil
}

func cmdImages(rt *runtime.Runtime, args []string) error {
	fs := newFlags("images", "[-q]")
	quiet := fs.Bool("q", false, "mostra só os IDs")
	asJSON := fs.Bool("json", false, "saída em JSON")
	fs.Parse(args)
	list, err := rt.Store.ListImages()
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(list)
	}
	if *quiet {
		for _, i := range list {
			fmt.Println(short(i.ID))
		}
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 3, ' ', 0)
	fmt.Fprintln(w, "IMAGEM\tID\tORIGEM\tCAMADAS\tTAMANHO\tCRIADA")
	for _, i := range list {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", i.Reference, short(i.ID), i.Source, i.Layers, human(uint64(i.SizeBytes)), ago(i.CreatedAt))
	}
	return w.Flush()
}

func cmdRmi(rt *runtime.Runtime, args []string) error {
	return each(args, func(ref string) error {
		img, err := rt.Images.Lookup(ref)
		if err != nil {
			return err
		}
		if _, err := rt.Images.Remove(img.ID); err != nil {
			return err
		}
		fmt.Println(img.Reference)
		return nil
	})
}

func cmdPrune(rt *runtime.Runtime, args []string) error {
	removed, err := rt.Prune()
	if err != nil {
		return err
	}
	for _, n := range removed {
		fmt.Println(n)
	}
	fmt.Fprintf(os.Stderr, "%d container(s) removido(s)\n", len(removed))
	return nil
}

func printEvent(e store.Event) {
	attrs := []string{}
	for k, v := range e.Attributes {
		attrs = append(attrs, k+"="+v)
	}
	sort.Strings(attrs)
	fmt.Printf("%s  %-9s %-10s %s %s\n", e.Time.Local().Format("2006-01-02 15:04:05"), e.Type, e.Action, e.ActorName, strings.Join(attrs, " "))
}

func cmdEvents(rt *runtime.Runtime, args []string) error {
	fs := newFlags("events", "[-f] [-n quantidade]")
	follow := fs.Bool("f", false, "acompanha novos eventos")
	n := fs.Int("n", 50, "quantidade de eventos anteriores")
	fs.Parse(args)
	list, err := rt.Store.Events(0, "", *n)
	if err != nil {
		return err
	}
	var last int64
	for _, e := range list {
		printEvent(e)
		last = e.ID
	}
	if !*follow {
		return nil
	}
	if last == 0 {
		last, _ = rt.Store.LastEventID()
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
		list, err := rt.Store.Events(last, "", 500)
		if err != nil {
			return err
		}
		for _, e := range list {
			printEvent(e)
			last = e.ID
		}
	}
}

func cmdInfo(rt *runtime.Runtime, args []string) error {
	info, err := rt.Info()
	if err != nil {
		return err
	}
	if len(args) > 0 && args[0] == "--json" {
		return printJSON(info)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "Versão:\t%s\n", info.Version)
	fmt.Fprintf(w, "Kernel:\t%s (%s)\n", info.Kernel, info.Arch)
	fmt.Fprintf(w, "Cgroups:\tv%d\n", info.CgroupVersion)
	fmt.Fprintf(w, "Armazenamento:\t%s\n", info.Storage)
	fmt.Fprintf(w, "Diretório:\t%s\n", info.Root)
	fmt.Fprintf(w, "Rede:\t%s (%s)\n", info.Bridge, info.Subnet)
	fmt.Fprintf(w, "CPUs:\t%d\n", info.CPUs)
	fmt.Fprintf(w, "Memória:\t%s\n", human(info.MemoryBytes))
	fmt.Fprintf(w, "Containers:\t%d (%d rodando, %d parados, %d criados)\n", info.Counts.Containers, info.Counts.Running, info.Counts.Exited, info.Counts.Created)
	fmt.Fprintf(w, "Imagens:\t%d\n", info.Counts.Images)
	return w.Flush()
}

func cmdServe(rt *runtime.Runtime, args []string) error {
	fs := newFlags("serve", "[--addr host:porta] [--token TOKEN]")
	addr := fs.String("addr", envOr("SILO_ADDR", "127.0.0.1:7070"), "endereço da API e da interface")
	token := fs.String("token", os.Getenv("SILO_TOKEN"), "token exigido nas requisições (Bearer)")
	fs.Parse(args)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return api.Serve(ctx, rt, api.Options{Addr: *addr, Token: *token})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
