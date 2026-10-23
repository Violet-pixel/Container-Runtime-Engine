package config

import (
	"os"
	"path/filepath"
	"strconv"
)

const Version = "1.1.0"

type Config struct {
	Root         string
	BridgeName   string
	BridgeCIDR   string
	Storage      string
	StopTimeout  int
	EventsRetain int
	DefaultDNS   []string
}

func Load() Config {
	c := Config{
		Root:         env("SILO_ROOT", "/var/lib/silo"),
		BridgeName:   env("SILO_BRIDGE", "silo0"),
		BridgeCIDR:   env("SILO_BRIDGE_CIDR", "10.88.0.1/16"),
		Storage:      env("SILO_STORAGE", "auto"),
		StopTimeout:  envInt("SILO_STOP_TIMEOUT", 10),
		EventsRetain: envInt("SILO_EVENTS_RETAIN", 5000),
		DefaultDNS:   []string{"1.1.1.1", "8.8.8.8"},
	}
	return c
}

func (c Config) DBPath() string        { return filepath.Join(c.Root, "silo.db") }
func (c Config) ImagesDir() string     { return filepath.Join(c.Root, "images") }
func (c Config) ContainersDir() string { return filepath.Join(c.Root, "containers") }
func (c Config) TmpDir() string        { return filepath.Join(c.Root, "tmp") }
func (c Config) ContainerDir(id string) string {
	return filepath.Join(c.ContainersDir(), id)
}

func (c Config) Ensure() error {
	for _, d := range []string{c.Root, c.ImagesDir(), c.ContainersDir(), c.TmpDir()} {
		if err := os.MkdirAll(d, 0o711); err != nil {
			return err
		}
	}
	return nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
