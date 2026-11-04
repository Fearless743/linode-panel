package sshrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// hostKeyCallback returns a HostKeyCallback honoring InsecureSkipVerify.
// Without a pin there is no way to accept a specific host key, so the safe
// default rejects every unknown host; opt-in accepts any (fresh provisioning).
func hostKeyCallback(insecure bool) ssh.HostKeyCallback {
	if insecure {
		return ssh.InsecureIgnoreHostKey()
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		return fmt.Errorf("sshrun: unknown host key for %s", hostname)
	}
}

// Run executes script on the remote host by piping it to the remote shell's
// stdin (`bash -s`). See the package contract in types.go for semantics.
func Run(ctx context.Context, opts Options, script string) (Result, error) {
	port := opts.Port
	if port == 0 {
		port = 22
	}
	user := opts.User
	if user == "" {
		user = "root"
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	// Timeout is the overall dial+exec deadline (frozen contract): bound the
	// whole operation, including the session, not just the TCP dial.
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if opts.Host == "" {
		return Result{}, errors.New("sshrun: host is required")
	}
	if opts.Password == "" {
		return Result{}, errors.New("sshrun: password is required")
	}

	cfg := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{ssh.Password(opts.Password)},
		// Freshly provisioned hosts present unknown host keys. The panel is
		// not a long-lived trust store, so host keys are only trusted when
		// the caller opts in to InsecureSkipVerify; otherwise reject all.
		HostKeyCallback: hostKeyCallback(opts.InsecureSkipVerify),
		Timeout:         timeout,
	}

	dialer := &net.Dialer{Timeout: timeout}
	addr := net.JoinHostPort(opts.Host, strconv.Itoa(port))
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Result{}, fmt.Errorf("sshrun: dial %s: %w", addr, err)
	}
	done := make(chan struct{})
	defer close(done)
	go func() { // honor ctx during the handshake too
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		return Result{}, fmt.Errorf("sshrun: handshake %s: %w", addr, err)
	}
	defer sshConn.Close()
	client := ssh.NewClient(sshConn, chans, reqs)

	sess, err := client.NewSession()
	if err != nil {
		return Result{}, fmt.Errorf("sshrun: new session: %w", err)
	}
	defer sess.Close()

	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	// StdinPipe must be created before Start: once Start marks the session
	// started, StdinPipe fails with "ssh: StdinPipe after process started".
	stdin, err := sess.StdinPipe()
	if err != nil {
		return Result{}, fmt.Errorf("sshrun: stdin pipe: %w", err)
	}
	if err := sess.Start("bash -s"); err != nil {
		return Result{}, fmt.Errorf("sshrun: start: %w", err)
	}
	go func() {
		_, _ = stdin.Write([]byte(script))
		_ = stdin.Close()
	}()
	// Exit code: Session.Wait returns nil only when the remote sent
	// exit-status 0; ExitError carries a non-zero status; ExitMissingError
	// means the session ended before any exit status arrived.
	exit := -1
	err = sess.Wait()
	if err == nil {
		exit = 0
	}
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		exit = ee.ExitStatus()
		err = nil
	}
	var ek *ssh.ExitMissingError
	if errors.As(err, &ek) {
		err = nil
	}
	// A cancelled/timed-out context aborts the session (the connection is
	// closed), surfacing as ExitMissingError above. That must be reported as
	// a failure, not a clean completion, in every phase.
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = ctxErr
	}
	res := Result{
		Command: fmt.Sprintf("ssh -p %d %s@%s 'bash -s'", port, user, opts.Host),
		Stdout:  strings.TrimRight(stdout.String(), "\n"),
		Stderr:  strings.TrimRight(stderr.String(), "\n"),
		Exit:    exit,
	}
	if err != nil {
		return res, fmt.Errorf("sshrun: session %s: %w", addr, err)
	}
	return res, nil
}
