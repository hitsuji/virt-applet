package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/digitalocean/go-libvirt"
)

// connect opens a libvirt connection for uri. Most transports are handled by
// go-libvirt; we special-case two so they behave like virsh:
//   - qemu+ssh:// shells out to the system ssh, honouring ~/.ssh/config
//     (go-libvirt's own SSH client remains available as qemu+libssh://).
//   - qemu:///session finds the per-user daemon socket.
func connect(uri *url.URL) (*libvirt.Libvirt, error) {
	switch {
	case uri.Scheme == "qemu+ssh":
		d := &sshDialer{uri: uri}
		lv := libvirt.NewWithDialer(d)
		if err := lv.ConnectToURI(libvirt.RemoteURI(uri)); err != nil {
			// go-libvirt only reports "procedure interrupted" when ssh dies;
			// ssh's own stderr says why (auth, DNS, host key, ...).
			if msg := d.stderr(); msg != "" {
				return nil, errors.New(msg)
			}
			return nil, err
		}
		return lv, nil
	case uri.Scheme == "qemu" && uri.Host == "" && uri.Path == "/session" && uri.Query().Get("socket") == "":
		u := *uri
		q := u.Query()
		q.Set("socket", sessionSocket())
		u.RawQuery = q.Encode()
		return libvirt.ConnectToURI(&u)
	default:
		return libvirt.ConnectToURI(uri)
	}
}

func sessionSocket() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	// Modular daemons (virtqemud) first, then monolithic libvirtd.
	for _, name := range []string{"virtqemud-sock", "libvirt-sock"} {
		p := filepath.Join(dir, "libvirt", name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join(dir, "libvirt", "virtqemud-sock")
}

type sshDialer struct {
	uri  *url.URL
	last *cmdConn
}

// stderr returns what the most recent ssh process printed, after giving it
// a moment to exit so the message is complete.
func (d *sshDialer) stderr() string {
	if d.last == nil {
		return ""
	}
	select {
	case <-d.last.exited:
	case <-time.After(2 * time.Second):
	}
	return strings.TrimSpace(d.last.stderr.String())
}

func (d *sshDialer) Dial() (net.Conn, error) {
	q := d.uri.Query()
	remoteURI := string(libvirt.RemoteURI(d.uri))
	sock := q.Get("socket")
	if sock == "" {
		sock = "/var/run/libvirt/libvirt-sock"
	}

	args := []string{"-T", "-o", "BatchMode=yes", "-e", "none"}
	if p := d.uri.Port(); p != "" {
		args = append(args, "-p", p)
	}
	if k := q.Get("keyfile"); k != "" {
		args = append(args, "-i", k)
	}
	host := d.uri.Hostname()
	if u := d.uri.User.Username(); u != "" {
		host = u + "@" + host
	}
	// Same fallback chain libvirt uses: virt-ssh-helper (libvirt >= 6.8),
	// otherwise netcat straight onto the daemon socket.
	remote := fmt.Sprintf(
		"if command -v virt-ssh-helper >/dev/null 2>&1; then exec virt-ssh-helper %s; else exec nc -U %s; fi",
		shellQuote(remoteURI), shellQuote(sock))
	args = append(args, host, "--", "sh", "-c", shellQuote(remote))

	cmd := exec.Command("ssh", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &lockedBuf{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &cmdConn{cmd: cmd, r: stdout, w: stdin, stderr: stderr, exited: make(chan struct{})}
	go func() {
		cmd.Wait()
		close(c.exited)
	}()
	d.last = c
	return c, nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// cmdConn presents a subprocess's stdin/stdout as a net.Conn.
type cmdConn struct {
	cmd    *exec.Cmd
	r      io.ReadCloser
	w      io.WriteCloser
	stderr *lockedBuf
	exited chan struct{}
}

// lockedBuf collects ssh's stderr; exec writes it from its own goroutine.
type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (c *cmdConn) Read(b []byte) (int, error) { return c.r.Read(b) }

func (c *cmdConn) Write(b []byte) (int, error) { return c.w.Write(b) }

func (c *cmdConn) Close() error {
	c.w.Close()
	c.cmd.Process.Kill()
	<-c.exited
	return nil
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "ssh" }

func (c *cmdConn) LocalAddr() net.Addr              { return pipeAddr{} }
func (c *cmdConn) RemoteAddr() net.Addr             { return pipeAddr{} }
func (c *cmdConn) SetDeadline(time.Time) error      { return nil }
func (c *cmdConn) SetReadDeadline(time.Time) error  { return nil }
func (c *cmdConn) SetWriteDeadline(time.Time) error { return nil }
