package runtime

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Violet-pixel/Container-Runtime-Engine/internal/store"
)

func ParsePort(s string) (store.PortMapping, error) {
	p := store.PortMapping{Protocol: "tcp", HostIP: "0.0.0.0"}
	if i := strings.LastIndex(s, "/"); i >= 0 {
		p.Protocol = strings.ToLower(s[i+1:])
		s = s[:i]
	}
	parts := strings.Split(s, ":")
	var host, cont string
	switch len(parts) {
	case 1:
		host, cont = parts[0], parts[0]
	case 2:
		host, cont = parts[0], parts[1]
	case 3:
		if net.ParseIP(parts[0]) == nil {
			return p, fmt.Errorf("%w: IP %q na porta", ErrInvalid, parts[0])
		}
		p.HostIP, host, cont = parts[0], parts[1], parts[2]
	default:
		return p, fmt.Errorf("%w: porta %q (use [ip:]host:container[/tcp|udp])", ErrInvalid, s)
	}
	var err error
	if p.HostPort, err = strconv.Atoi(host); err != nil {
		return p, fmt.Errorf("%w: porta %q", ErrInvalid, host)
	}
	if p.ContainerPort, err = strconv.Atoi(cont); err != nil {
		return p, fmt.Errorf("%w: porta %q", ErrInvalid, cont)
	}
	return p, nil
}

func ParseMount(s string) (store.Mount, error) {
	parts := strings.Split(s, ":")
	m := store.Mount{}
	switch len(parts) {
	case 2:
		m.Source, m.Target = parts[0], parts[1]
	case 3:
		m.Source, m.Target = parts[0], parts[1]
		switch parts[2] {
		case "ro":
			m.ReadOnly = true
		case "rw":
		default:
			return m, fmt.Errorf("%w: modo de volume %q (use ro ou rw)", ErrInvalid, parts[2])
		}
	default:
		return m, fmt.Errorf("%w: volume %q (use origem:destino[:ro])", ErrInvalid, s)
	}
	if abs, err := filepath.Abs(m.Source); err == nil {
		m.Source = abs
	}
	return m, nil
}

func ParseMemory(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "0" {
		return 0, nil
	}
	mult := 1.0 / (1024 * 1024)
	switch {
	case strings.HasSuffix(s, "g"), strings.HasSuffix(s, "gb"):
		mult = 1024
		s = strings.TrimRight(s, "gb")
	case strings.HasSuffix(s, "m"), strings.HasSuffix(s, "mb"):
		mult = 1
		s = strings.TrimRight(s, "mb")
	case strings.HasSuffix(s, "k"), strings.HasSuffix(s, "kb"):
		mult = 1.0 / 1024
		s = strings.TrimRight(s, "kb")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("%w: memória %q (ex.: 256m, 1g)", ErrInvalid, s)
	}
	return int64(v * mult), nil
}

func ParseRestart(s string) (store.RestartPolicy, error) {
	name, max, _ := strings.Cut(s, ":")
	rp := store.RestartPolicy{Name: name}
	if max != "" {
		n, err := strconv.Atoi(max)
		if err != nil || n < 0 {
			return rp, fmt.Errorf("%w: reinício %q", ErrInvalid, s)
		}
		rp.MaxRetries = n
	}
	return rp, nil
}
