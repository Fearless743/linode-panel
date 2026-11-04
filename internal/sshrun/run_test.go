package sshrun

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startServer runs an in-memory SSH server accepting exactly wantPassword.
// The handler records the script read from stdin and responds with fixed
// stdout/stderr and the given exit code.
func startServer(t *testing.T, wantPassword string, scriptCh chan<- string, exitCode int) (host string, port int) {
	t.Helper()

	hostSigner, err := newTestSigner()
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) != wantPassword {
				return nil, fmt.Errorf("wrong password")
			}
			return nil, nil
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveConn(conn, cfg, scriptCh, exitCode)
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func serveConn(netConn net.Conn, cfg *ssh.ServerConfig, scriptCh chan<- string, exitCode int) {
	sshConn, chans, reqs, err := ssh.NewServerConn(netConn, cfg)
	if err != nil {
		_ = netConn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "only session")
			continue
		}
		ch, chReqs, err := newChan.Accept()
		if err != nil {
			continue
		}
		go serveSession(ch, chReqs, scriptCh, exitCode)
	}
	_ = sshConn.Close()
}

func serveSession(ch ssh.Channel, reqs <-chan *ssh.Request, scriptCh chan<- string, exitCode int) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "exec":
			// The client runs "bash -s"; the script arrives on stdin.
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
			script, _ := io.ReadAll(ch)
			if scriptCh != nil {
				scriptCh <- string(script)
			}
			_, _ = io.WriteString(ch, "fixed-stdout\n")
			_, _ = io.WriteString(ch.Stderr(), "fixed-stderr\n")
			// Always report an exit status so the client observes a real
			// remote exit code (0 included), matching a real SSH server.
			status := struct{ Status uint32 }{uint32(exitCode)}
			_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(&status))
			_ = ch.Close()
			return
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

func newTestSigner() (ssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}

// TestRunScriptDelivered verifies that the script reaches the remote over
// stdin and that Result captures stdout, stderr, exit code and Command.
func TestRunScriptDelivered(t *testing.T) {
	const password = "Secret123!"

	scriptCh := make(chan string, 1)
	host, port := startServer(t, password, scriptCh, 7)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := Run(ctx, Options{
		Host:               host,
		Port:               port,
		User:               "root",
		Password:           password,
		InsecureSkipVerify: true,
	}, "echo hello\n")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := <-scriptCh; got != "echo hello\n" {
		t.Errorf("script on remote = %q, want %q", got, "echo hello\n")
	}
	if res.Stdout != "fixed-stdout" {
		t.Errorf("stdout = %q, want %q", res.Stdout, "fixed-stdout")
	}
	if res.Stderr != "fixed-stderr" {
		t.Errorf("stderr = %q, want %q", res.Stderr, "fixed-stderr")
	}
	if res.Exit != 7 {
		t.Errorf("exit = %d, want 7", res.Exit)
	}
	wantCmd := fmt.Sprintf("ssh -p %d root@%s 'bash -s'", port, host)
	if res.Command != wantCmd {
		t.Errorf("command = %q, want %q", res.Command, wantCmd)
	}
}

// TestRunNonZeroExit verifies the contract: a non-zero remote exit code is not
// an error; it is reported via Result.Exit.
func TestRunNonZeroExit(t *testing.T) {
	const password = "Secret123!"
	host, port := startServer(t, password, nil, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := Run(ctx, Options{
		Host:               host,
		Port:               port,
		User:               "root",
		Password:           password,
		InsecureSkipVerify: true,
	}, "exit 3")
	if err != nil {
		t.Fatalf("Run: expected nil error for non-zero exit, got %v", err)
	}
	if res.Exit != 3 {
		t.Errorf("exit = %d, want 3", res.Exit)
	}
}

// TestRunRejectsUnknownHostKey verifies the secure default: without
// InsecureSkipVerify, an unknown host key is rejected and Run errors.
func TestRunRejectsUnknownHostKey(t *testing.T) {
	const password = "Secret123!"
	host, port := startServer(t, password, nil, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Run(ctx, Options{
		Host:     host,
		Port:     port,
		User:     "root",
		Password: password,
		// InsecureSkipVerify left false: unknown host key must be rejected.
	}, "echo hi")
	if err == nil {
		t.Fatalf("Run: expected unknown-host-key error, got nil")
	}
}

// TestRunAuthFailure verifies a wrong password yields a non-nil error.
func TestRunAuthFailure(t *testing.T) {
	host, port := startServer(t, "correct-password", nil, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Run(ctx, Options{
		Host:               host,
		Port:               port,
		User:               "root",
		Password:           "wrong-password",
		InsecureSkipVerify: true,
	}, "echo hi")
	if err == nil {
		t.Fatalf("Run: expected auth failure error, got nil")
	}
}

// TestRunConnectionRefused verifies dial failures are returned as errors.
func TestRunConnectionRefused(t *testing.T) {
	// Grab a port and close it to guarantee refusal.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = Run(ctx, Options{
		Host:               "127.0.0.1",
		Port:               port,
		User:               "root",
		Password:           "Secret123!",
		InsecureSkipVerify: true,
	}, "echo hi")
	if err == nil {
		t.Fatalf("Run: expected connection refused error, got nil")
	}
}

// TestRunContextCancel verifies a cancelled context aborts the operation.
func TestRunContextCancel(t *testing.T) {
	// A listener that accepts but never completes the handshake, so the
	// client blocks in the SSH handshake until ctx cancels.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold the conn open without speaking SSH.
			defer conn.Close()
			<-time.After(5 * time.Second)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	_, err = Run(ctx, Options{
		Host:               "127.0.0.1",
		Port:               ln.Addr().(*net.TCPAddr).Port,
		User:               "root",
		Password:           "Secret123!",
		InsecureSkipVerify: true,
	}, "echo hi")
	if err == nil {
		t.Fatalf("Run: expected context cancellation error, got nil")
	}
}

// TestRunTimeoutBoundsSession verifies the frozen contract that Timeout is
// the overall dial+exec deadline: a remote command that never exits must be
// aborted, not block forever.
func TestRunTimeoutBoundsSession(t *testing.T) {
	const password = "Secret123!"
	hostSigner, err := newTestSigner()
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if string(pass) != password {
				return nil, fmt.Errorf("wrong password")
			}
			return nil, nil
		},
	}
	cfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(nc net.Conn) {
				sc, chans, reqs, err := ssh.NewServerConn(nc, cfg)
				if err != nil {
					_ = nc.Close()
					return
				}
				go ssh.DiscardRequests(reqs)
				for newChan := range chans {
					ch, chReqs, err := newChan.Accept()
					if err != nil {
						continue
					}
					// Acknowledge exec but never send exit-status: the remote
					// command hangs, so the client must be bounded by Timeout.
					go func(ch ssh.Channel, reqs <-chan *ssh.Request) {
						defer ch.Close()
						for req := range reqs {
							if req.Type == "exec" && req.WantReply {
								_ = req.Reply(true, nil)
							} else if req.WantReply {
								_ = req.Reply(false, nil)
							}
						}
					}(ch, chReqs)
				}
				_ = sc.Close()
			}(conn)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err = Run(ctx, Options{
		Host:               "127.0.0.1",
		Port:               ln.Addr().(*net.TCPAddr).Port,
		User:               "root",
		Password:           password,
		InsecureSkipVerify: true,
		Timeout:            500 * time.Millisecond,
	}, "sleep 60")
	if err == nil {
		t.Fatalf("Run: expected timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Run took %v to abort, want bounded by ~Timeout", elapsed)
	}
}

// TestRunMissingPassword verifies the missing-password guard.
func TestRunMissingPassword(t *testing.T) {
	_, err := Run(context.Background(), Options{Host: "1.2.3.4"}, "echo hi")
	if err == nil {
		t.Fatalf("Run: expected missing password error, got nil")
	}
}

// TestRunZeroExit verifies exit code 0 is recorded as 0.
func TestRunZeroExit(t *testing.T) {
	const password = "Secret123!"
	host, port := startServer(t, password, nil, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := Run(ctx, Options{
		Host:               host,
		Port:               port,
		User:               "root",
		Password:           password,
		InsecureSkipVerify: true,
	}, "true")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Exit != 0 {
		t.Errorf("exit = %d, want 0", res.Exit)
	}
}

// TestRunContextAlreadyCancelled verifies a pre-cancelled context errors out
// before dialing.
func TestRunContextAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, Options{
		Host:               "127.0.0.1",
		Port:               22,
		User:               "root",
		Password:           "Secret123!",
		InsecureSkipVerify: true,
	}, "echo hi")
	if err == nil {
		t.Fatalf("Run: expected context cancelled error, got nil")
	}
}
