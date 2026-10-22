package network

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ModeBridge = "bridge"
	ModeHost   = "host"
	ModeNone   = "none"
)

type Bridge struct {
	Name    string
	Gateway net.IP
	Subnet  *net.IPNet
}

func ParseBridge(name, cidr string) (*Bridge, error) {
	ip, subnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("SILO_BRIDGE_CIDR inválido: %w", err)
	}
	return &Bridge{Name: name, Gateway: ip.To4(), Subnet: subnet}, nil
}

func ValidMode(m string) bool {
	return m == ModeBridge || m == ModeHost || m == ModeNone
}

func run(name string, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func inNetns(pid int, args ...string) error {
	full := append([]string{"-t", strconv.Itoa(pid), "-n", "--"}, args...)
	return run("nsenter", full...)
}

func Requirements(mode string) error {
	if mode != ModeBridge && mode != ModeNone {
		return nil
	}
	for _, bin := range []string{"ip", "nsenter"} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("o comando %q não foi encontrado (instale iproute2 e util-linux)", bin)
		}
	}
	return nil
}

func (b *Bridge) Ensure() error {
	if _, err := net.InterfaceByName(b.Name); err != nil {
		if err := run("ip", "link", "add", b.Name, "type", "bridge"); err != nil {
			return err
		}
	}
	ones, _ := b.Subnet.Mask.Size()
	addr := fmt.Sprintf("%s/%d", b.Gateway, ones)
	if !hasAddr(b.Name, b.Gateway) {
		if err := run("ip", "addr", "add", addr, "dev", b.Name); err != nil && !strings.Contains(err.Error(), "File exists") {
			return err
		}
	}
	if err := run("ip", "link", "set", b.Name, "up"); err != nil {
		return err
	}
	_ = os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644)
	if _, err := exec.LookPath("iptables"); err == nil {
		ensureRule("nat", "POSTROUTING", "-s", b.Subnet.String(), "!", "-o", b.Name, "-j", "MASQUERADE")
		ensureRule("filter", "FORWARD", "-i", b.Name, "-j", "ACCEPT")
		ensureRule("filter", "FORWARD", "-o", b.Name, "-j", "ACCEPT")
	}
	return nil
}

func hasAddr(iface string, ip net.IP) bool {
	i, err := net.InterfaceByName(iface)
	if err != nil {
		return false
	}
	addrs, _ := i.Addrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

func ensureRule(table, chain string, rule ...string) {
	check := append([]string{"-w", "-t", table, "-C", chain}, rule...)
	if exec.Command("iptables", check...).Run() == nil {
		return
	}
	add := append([]string{"-w", "-t", table, "-I", chain, "1"}, rule...)
	_ = run("iptables", add...)
}

func ipToInt(ip net.IP) uint32 { return binary.BigEndian.Uint32(ip.To4()) }

func intToIP(n uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, n)
	return ip
}

func (b *Bridge) Allocate(used map[string]bool) (net.IP, error) {
	start := ipToInt(b.Subnet.IP) + 1
	ones, bits := b.Subnet.Mask.Size()
	size := uint32(1) << uint(bits-ones)
	end := ipToInt(b.Subnet.IP) + size - 1
	for n := start; n < end; n++ {
		ip := intToIP(n)
		if ip.Equal(b.Gateway) || used[ip.String()] {
			continue
		}
		return ip, nil
	}
	return nil, fmt.Errorf("sem endereços livres em %s", b.Subnet)
}

func vethNames(id string) (string, string) {
	short := id
	if len(short) > 10 {
		short = short[:10]
	}
	return "vs" + short, "vc" + short
}

func (b *Bridge) Attach(pid int, id string, ip net.IP) error {
	host, peer := vethNames(id)
	_ = run("ip", "link", "del", host)
	if err := run("ip", "link", "add", host, "type", "veth", "peer", "name", peer); err != nil {
		return err
	}
	steps := [][]string{
		{"ip", "link", "set", host, "master", b.Name},
		{"ip", "link", "set", host, "up"},
		{"ip", "link", "set", peer, "netns", strconv.Itoa(pid)},
	}
	for _, s := range steps {
		if err := run(s[0], s[1:]...); err != nil {
			_ = run("ip", "link", "del", host)
			return err
		}
	}
	ones, _ := b.Subnet.Mask.Size()
	inner := [][]string{
		{"ip", "link", "set", "lo", "up"},
		{"ip", "link", "set", peer, "name", "eth0"},
		{"ip", "addr", "add", fmt.Sprintf("%s/%d", ip, ones), "dev", "eth0"},
		{"ip", "link", "set", "eth0", "up"},
		{"ip", "route", "add", "default", "via", b.Gateway.String()},
	}
	for _, s := range inner {
		if err := inNetns(pid, s...); err != nil {
			return err
		}
	}
	return nil
}

func LoopbackOnly(pid int) error {
	return inNetns(pid, "ip", "link", "set", "lo", "up")
}

func Detach(id string) {
	host, _ := vethNames(id)
	if _, err := net.InterfaceByName(host); err == nil {
		_ = run("ip", "link", "del", host)
	}
}

type Counters struct {
	RxBytes   uint64 `json:"rxBytes"`
	TxBytes   uint64 `json:"txBytes"`
	RxPackets uint64 `json:"rxPackets"`
	TxPackets uint64 `json:"txPackets"`
}

func ReadCounters(pid int) Counters {
	var c Counters
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/net/dev", pid))
	if err != nil {
		return c
	}
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if name == "lo" {
			continue
		}
		f := strings.Fields(parts[1])
		if len(f) < 10 {
			continue
		}
		rx, _ := strconv.ParseUint(f[0], 10, 64)
		rxp, _ := strconv.ParseUint(f[1], 10, 64)
		tx, _ := strconv.ParseUint(f[8], 10, 64)
		txp, _ := strconv.ParseUint(f[9], 10, 64)
		c.RxBytes += rx
		c.RxPackets += rxp
		c.TxBytes += tx
		c.TxPackets += txp
	}
	return c
}

type Proxy struct {
	listeners []io.Closer
	wg        sync.WaitGroup
}

func StartProxy(hostIP string, hostPort int, target string, proto string) (*Proxy, error) {
	p := &Proxy{}
	if hostIP == "" {
		hostIP = "0.0.0.0"
	}
	addr := net.JoinHostPort(hostIP, strconv.Itoa(hostPort))
	switch proto {
	case "udp":
		pc, err := net.ListenPacket("udp", addr)
		if err != nil {
			return nil, err
		}
		p.listeners = append(p.listeners, pc)
		p.wg.Add(1)
		go func() { defer p.wg.Done(); udpLoop(pc, target) }()
	default:
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, err
		}
		p.listeners = append(p.listeners, ln)
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				go tcpPipe(conn, target)
			}
		}()
	}
	return p, nil
}

func (p *Proxy) Close() {
	for _, l := range p.listeners {
		l.Close()
	}
	p.wg.Wait()
}

func tcpPipe(in net.Conn, target string) {
	defer in.Close()
	out, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		return
	}
	defer out.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(out, in); closeWrite(out); done <- struct{}{} }()
	go func() { io.Copy(in, out); closeWrite(in); done <- struct{}{} }()
	<-done
	<-done
}

func closeWrite(c net.Conn) {
	if t, ok := c.(*net.TCPConn); ok {
		t.CloseWrite()
	}
}

func udpLoop(pc net.PacketConn, target string) {
	raddr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		return
	}
	var mu sync.Mutex
	sessions := map[string]*net.UDPConn{}
	buf := make([]byte, 65535)
	for {
		n, client, err := pc.ReadFrom(buf)
		if err != nil {
			mu.Lock()
			for _, c := range sessions {
				c.Close()
			}
			mu.Unlock()
			return
		}
		key := client.String()
		mu.Lock()
		conn, ok := sessions[key]
		if !ok {
			conn, err = net.DialUDP("udp", nil, raddr)
			if err != nil {
				mu.Unlock()
				continue
			}
			sessions[key] = conn
			go func(c *net.UDPConn, client net.Addr, key string) {
				rb := make([]byte, 65535)
				for {
					c.SetReadDeadline(time.Now().Add(2 * time.Minute))
					m, err := c.Read(rb)
					if err != nil {
						mu.Lock()
						delete(sessions, key)
						mu.Unlock()
						c.Close()
						return
					}
					pc.WriteTo(rb[:m], client)
				}
			}(conn, client, key)
		}
		mu.Unlock()
		conn.Write(buf[:n])
	}
}

func ResolvConf(dns []string, fallback []string) []byte {
	if len(dns) > 0 {
		return nameservers(dns)
	}
	for _, path := range []string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"} {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var ns []string
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 && f[0] == "nameserver" {
				ip := net.ParseIP(f[1])
				if ip != nil && !ip.IsLoopback() {
					ns = append(ns, f[1])
				}
			}
		}
		if len(ns) > 0 {
			return nameservers(ns)
		}
	}
	return nameservers(fallback)
}

func nameservers(list []string) []byte {
	var b strings.Builder
	for _, n := range list {
		fmt.Fprintf(&b, "nameserver %s\n", n)
	}
	return []byte(b.String())
}
