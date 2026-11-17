package lish

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"linode-panel/internal/linode"
)

// scriptedConn emulates a Lish console: it emits a login prompt on connect
// and answers each keystroke batch with the expected console response.
type scriptedConn struct {
	out     chan []byte
	pass    string
	badPass bool
	exit    int
}

func newScriptedConn(pass string, badPass bool, exit int) *scriptedConn {
	c := &scriptedConn{
		out:     make(chan []byte, 64),
		pass:    pass,
		badPass: badPass,
		exit:    exit,
	}
	c.out <- []byte("debian login: ")
	return c
}

var (
	readyCmdRE = regexp.MustCompile(`echo (__PANEL_BOOT_[0-9a-f]+_READY__)`)
	rcCmdRE    = regexp.MustCompile(`(__PANEL_BOOT_[0-9a-f]+_RC__):%d__`)
)

func (c *scriptedConn) Read(ctx context.Context) ([]byte, error) {
	select {
	case b := <-c.out:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *scriptedConn) Write(_ context.Context, data []byte) error {
	cmd := strings.TrimRight(string(data), "\r")
	switch {
	case cmd == "root":
		c.out <- []byte("Password: ")
	case cmd == c.pass:
		if c.badPass {
			c.out <- []byte("Login incorrect\r\ndebian login: ")
		} else {
			c.out <- []byte("\r\nWelcome to Debian\r\nroot@li:~# ")
		}
	case strings.HasPrefix(cmd, "echo __PANEL_BOOT_"):
		m := readyCmdRE.FindStringSubmatch(cmd)
		if m != nil {
			c.out <- []byte(m[1] + "\r\nroot@li:~# ")
		}
	case strings.Contains(cmd, "_RC__:%d__"):
		m := rcCmdRE.FindStringSubmatch(cmd)
		if m != nil {
			c.out <- []byte("SCRIPT-OUT\r\n" + m[1] + ":" + itoa(c.exit) + "__\r\nroot@li:~# ")
		}
	default:
		// stty, file creation, chunk writes, cleanup: just return a prompt.
		c.out <- []byte("root@li:~# ")
	}
	return nil
}

func (c *scriptedConn) Close() error { return nil }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func testOptions(conn Conn) Options {
	return Options{
		InstanceID: 42,
		PAT:        "pat",
		Password:   "Secret123!",
		Timeout:    10 * time.Second,
		API: func(context.Context, int) (linode.LishData, error) {
			return linode.LishData{WeblishURL: "wss://lish.example.com/websocket/lish/token", WSProtocols: []string{"binary"}}, nil
		},
		Open: func(context.Context, string, []string) (Conn, error) { return conn, nil },
	}
}

// TestRunDeliversScriptAndExitCode verifies the full console flow: login,
// shell readiness, chunked script delivery and exit-code capture.
func TestRunDeliversScriptAndExitCode(t *testing.T) {
	conn := newScriptedConn("Secret123!", false, 7)
	res, err := Run(context.Background(), testOptions(conn), "#!/bin/bash\necho hi\n")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Exit != 7 {
		t.Fatalf("Exit = %d, want 7", res.Exit)
	}
	if !strings.Contains(res.Stdout, "SCRIPT-OUT") {
		t.Fatalf("Stdout = %q, want script output", res.Stdout)
	}
	if res.Host != "lish.example.com" {
		t.Fatalf("Host = %q, want lish.example.com", res.Host)
	}
	if !strings.Contains(res.Command, "42") {
		t.Fatalf("Command = %q, want instance id", res.Command)
	}
}

// TestRunZeroExit verifies a successful (zero) exit code is reported as 0.
func TestRunZeroExit(t *testing.T) {
	conn := newScriptedConn("Secret123!", false, 0)
	res, err := Run(context.Background(), testOptions(conn), "echo ok")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Exit != 0 {
		t.Fatalf("Exit = %d, want 0", res.Exit)
	}
}

// TestRunAuthFailure verifies a rejected root password is surfaced as an
// auth error (which is not safe to retry).
func TestRunAuthFailure(t *testing.T) {
	conn := newScriptedConn("Secret123!", true, 0)
	_, err := Run(context.Background(), testOptions(conn), "echo ok")
	if err == nil {
		t.Fatal("Run: expected auth error, got nil")
	}
	if !IsAuthError(err) {
		t.Fatalf("IsAuthError(%v) = false, want true", err)
	}
}

// TestRunValidation checks the required-option guards.
func TestRunValidation(t *testing.T) {
	if _, err := Run(context.Background(), Options{}, "echo hi"); err == nil {
		t.Fatal("expected error for missing instance id")
	}
	if _, err := Run(context.Background(), Options{InstanceID: 1}, "echo hi"); err == nil {
		t.Fatal("expected error for missing password")
	}
	if _, err := Run(context.Background(), Options{InstanceID: 1, Password: "p"}, "echo hi"); err == nil {
		t.Fatal("expected error for missing PAT")
	}
}
