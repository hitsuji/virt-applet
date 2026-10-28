package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// connConfig is one libvirt target shown in the menu.
type connConfig struct {
	Name string
	URI  *url.URL
}

func defaultConfigPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "kvm-applet", "connections")
}

// loadConfig reads a connections file. Each non-comment line is either
//
//	<uri>
//	<name> <uri>
//
// A missing file is not an error; it just yields no connections.
func loadConfig(path string) ([]connConfig, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	defer f.Close()

	var conns []connConfig
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		var name, raw string
		switch len(fields) {
		case 1:
			raw = fields[0]
		case 2:
			name, raw = fields[0], fields[1]
		default:
			return nil, fmt.Errorf("%s:%d: expected \"[name] uri\"", path, n)
		}
		c, err := parseConn(name, raw)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		conns = append(conns, c)
	}
	return conns, sc.Err()
}

func parseConn(name, raw string) (connConfig, error) {
	u, err := url.Parse(os.ExpandEnv(raw))
	if err != nil {
		return connConfig{}, err
	}
	if u.Scheme == "" {
		return connConfig{}, fmt.Errorf("%q is not a libvirt URI (e.g. qemu:///system)", raw)
	}
	if name == "" {
		name = defaultName(u)
	}
	return connConfig{Name: name, URI: u}, nil
}

func defaultName(u *url.URL) string {
	if h := u.Hostname(); h != "" {
		return h
	}
	if strings.Trim(u.Path, "/") == "session" {
		return "session"
	}
	return "local"
}
