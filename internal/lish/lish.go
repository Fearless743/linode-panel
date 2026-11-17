package lish

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"linode-panel/internal/linode"
)

const (
	defaultTimeout  = 5 * time.Minute
	loginTimeout    = 2 * time.Minute
	passwordTimeout = time.Minute
	promptTimeout   = time.Minute
	readyTimeout    = 30 * time.Second
	scriptTimeout   = 4 * time.Minute
	// chunkSize stays well below a PTY's canonical line limit (4096 on Linux).
	chunkSize = 512
)

var (
	loginPromptRE    = regexp.MustCompile(`(?i)login\s*:`)
	passwordPromptRE = regexp.MustCompile(`(?i)password\s*:`)
	loginFailedRE    = regexp.MustCompile(`(?i)(login incorrect|authentication failure|permission denied)`)
	// The prompt may carry ANSI colour sequences after the '#'/'$' marker.
	shellPromptRE = regexp.MustCompile("(?m)[^\r\n]{0,200}[#$](?:\\x1b\\[[0-9;?]*[a-zA-Z])? ?\\r?$")
	ansiRE        = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))`)
)

// Run executes script on the instance's Lish console. See the package
// contract in types.go for the exact semantics.
func Run(ctx context.Context, opts Options, script string) (Result, error) {
	if opts.InstanceID <= 0 {
		return Result{}, errors.New("lish: instance id is required")
	}
	if opts.Password == "" {
		return Result{}, errors.New("lish: root password is required")
	}
	if opts.API == nil && opts.PAT == "" {
		return Result{}, errors.New("lish: PAT is required")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, timeout)
	defer cancel()

	data, err := lishData(ctx, opts)
	if err != nil {
		return Result{}, fmt.Errorf("lish: get console credentials: %w", err)
	}
	if data.WeblishURL == "" {
		return Result{}, errors.New("lish: weblish url is empty")
	}

	conn, err := openConsole(ctx, opts, data)
	if err != nil {
		return Result{}, err
	}
	// Canceling ctx unblocks a pending websocket read so the driver can exit
	// promptly on timeout instead of waiting for the read deadline.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	defer conn.Close()

	res, err := drive(ctx, conn, opts.Password, script)
	if err != nil {
		return Result{}, err
	}
	res.Host = hostOf(data.WeblishURL)
	res.Command = fmt.Sprintf("lish console %d (%s)", opts.InstanceID, res.Host)
	return res, nil
}

func lishData(ctx context.Context, opts Options) (linode.LishData, error) {
	if opts.API != nil {
		return opts.API(ctx, opts.InstanceID)
	}
	client := linode.New(opts.PAT)
	if opts.APIBase != "" {
		client.BaseURL = strings.TrimRight(opts.APIBase, "/")
	}
	return client.GetInstanceLish(ctx, opts.InstanceID)
}

func openConsole(ctx context.Context, opts Options, data linode.LishData) (Conn, error) {
	if opts.Open != nil {
		conn, err := opts.Open(ctx, data.WeblishURL, data.WSProtocols)
		if err != nil {
			return nil, fmt.Errorf("lish: dial: %w", err)
		}
		return conn, nil
	}
	dialer := websocket.Dialer{
		Subprotocols:     data.WSProtocols,
		HandshakeTimeout: 30 * time.Second,
	}
	ws, resp, err := dialer.DialContext(ctx, data.WeblishURL, nil)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("lish: handshake status %d: %w", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("lish: dial: %w", err)
	}
	ws.SetReadLimit(8 << 20)
	return &wsConn{ws: ws}, nil
}

// wsConn adapts gorilla/websocket to the Conn interface.
type wsConn struct {
	ws *websocket.Conn
}

func (w *wsConn) Read(ctx context.Context) ([]byte, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = w.ws.SetReadDeadline(deadline)
	}
	_, msg, err := w.ws.ReadMessage()
	return msg, err
}

func (w *wsConn) Write(ctx context.Context, data []byte) error {
	if deadline, ok := ctx.Deadline(); ok {
		_ = w.ws.SetWriteDeadline(deadline)
	}
	return w.ws.WriteMessage(websocket.TextMessage, data)
}

func (w *wsConn) Close() error { return w.ws.Close() }

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Host
}

type readResult struct {
	data []byte
	err  error
}

// console buffers everything the remote sends and lets the driver wait for
// regexes without losing output that arrived before the wait began.
type console struct {
	conn Conn
	ch   chan readResult
	buf  []byte
	pos  int
}

func (c *console) pump(ctx context.Context) {
	defer close(c.ch)
	for {
		data, err := c.conn.Read(ctx)
		select {
		case c.ch <- readResult{data: data, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func (c *console) waitFor(ctx context.Context, re *regexp.Regexp, timeout time.Duration) ([]byte, error) {
	match, _, err := c.waitForOr(ctx, re, nil, timeout)
	return match, err
}

// waitForOr is waitFor with an early-abort pattern. It reports whether abort
// matched, letting the caller fail fast (e.g. on "Login incorrect") instead
// of waiting for the full timeout.
func (c *console) waitForOr(ctx context.Context, re, abort *regexp.Regexp, timeout time.Duration) ([]byte, bool, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		if abort != nil && abort.Match(c.buf[c.pos:]) {
			return nil, true, nil
		}
		if loc := re.FindIndex(c.buf[c.pos:]); loc != nil {
			start := c.pos + loc[0]
			c.pos += loc[1]
			return c.buf[start:c.pos], false, nil
		}
		select {
		case r := <-c.ch:
			if r.err != nil {
				return nil, false, r.err
			}
			c.buf = append(c.buf, r.data...)
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-timer.C:
			return nil, false, fmt.Errorf("lish: 等待 %s 超时", re.String())
		}
	}
}

// mark resets the scan position to the end of the received data and returns
// the previous position, so the caller can capture output produced after it.
func (c *console) mark() int {
	c.pos = len(c.buf)
	return c.pos
}

func (c *console) send(ctx context.Context, s string) error {
	if err := c.conn.Write(ctx, []byte(s)); err != nil {
		return fmt.Errorf("lish: write: %w", err)
	}
	return nil
}

func (c *console) hasLoginFailure() bool {
	return loginFailedRE.Match(c.buf)
}

func drive(ctx context.Context, conn Conn, password, script string) (Result, error) {
	nonce := randomHex(8)
	c := &console{conn: conn, ch: make(chan readResult, 64)}
	go c.pump(ctx)

	// 1. Log in as root over the serial console.
	if _, err := c.waitFor(ctx, loginPromptRE, loginTimeout); err != nil {
		return Result{}, fmt.Errorf("lish: 等待登录提示失败: %w", err)
	}
	if err := c.send(ctx, "root\r"); err != nil {
		return Result{}, err
	}
	if _, err := c.waitFor(ctx, passwordPromptRE, passwordTimeout); err != nil {
		return Result{}, fmt.Errorf("lish: 等待密码提示失败: %w", err)
	}
	if err := c.send(ctx, password+"\r"); err != nil {
		return Result{}, err
	}
	if _, failed, err := c.waitForOr(ctx, shellPromptRE, loginFailedRE, promptTimeout); err != nil {
		return Result{}, fmt.Errorf("lish: 等待 shell 提示符失败: %w", err)
	} else if failed || c.hasLoginFailure() {
		return Result{}, errors.New("lish: auth: root 密码不正确或控制台登录失败")
	}

	// 2. Confirm the shell is interactive, then disable echo so the captured
	//    transcript contains the script's output rather than typed commands.
	ready := "__PANEL_BOOT_" + nonce + "_READY__"
	c.mark()
	if err := c.send(ctx, "echo "+ready+"\r"); err != nil {
		return Result{}, err
	}
	if _, err := c.waitFor(ctx, regexp.MustCompile(regexp.QuoteMeta(ready)), readyTimeout); err != nil {
		return Result{}, fmt.Errorf("lish: shell 未响应: %w", err)
	}
	c.mark()
	if err := c.send(ctx, "stty -echo\r"); err != nil {
		return Result{}, err
	}
	if _, err := c.waitFor(ctx, shellPromptRE, readyTimeout); err != nil {
		return Result{}, fmt.Errorf("lish: 关闭回显失败: %w", err)
	}

	// 3. Write the base64-encoded script to a temp file in small chunks.
	//    Chunking avoids PTY canonical line limits and shell quoting problems.
	//    Each chunk is acknowledged by its own marker so the next one is only
	//    sent once the previous command has actually executed — waiting for a
	//    bare shell prompt is not reliable on a serial console.
	b64Path := "/tmp/.panel-boot-" + nonce + ".b64"
	scriptPath := "/tmp/.panel-boot-" + nonce + ".sh"
	c.mark()
	if err := c.send(ctx, ": > '"+b64Path+"'\r"); err != nil {
		return Result{}, err
	}
	if _, err := c.waitFor(ctx, shellPromptRE, readyTimeout); err != nil {
		return Result{}, fmt.Errorf("lish: 准备脚本文件失败: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	for i := 0; i < len(encoded); i += chunkSize {
		j := i + chunkSize
		if j > len(encoded) {
			j = len(encoded)
		}
		ack := fmt.Sprintf("__PANEL_CHUNK_%s_%d__", nonce, i/chunkSize)
		c.mark()
		if err := c.send(ctx, fmt.Sprintf("printf %%s '%s' >> '%s'; printf '\\n%s\\n'\r", encoded[i:j], b64Path, ack)); err != nil {
			return Result{}, err
		}
		if _, err := c.waitFor(ctx, regexp.MustCompile(regexp.QuoteMeta(ack)), readyTimeout); err != nil {
			return Result{}, fmt.Errorf("lish: 写入脚本第 %d 段失败: %w", i/chunkSize+1, err)
		}
	}

	// 4. Decode and verify the byte count before running. A truncated transfer
	//    must never be executed as a partial script.
	lenMarker := "__PANEL_LEN_" + nonce + "__"
	lenRE := regexp.MustCompile(regexp.QuoteMeta(lenMarker) + `:(\d+):(\d+)__`)
	c.mark()
	if err := c.send(ctx, fmt.Sprintf("base64 -d '%s' > '%s'; printf '\\n%s:%%d:%%d__\\n' $(wc -c < '%s') %d\r", b64Path, scriptPath, lenMarker, scriptPath, len(script))); err != nil {
		return Result{}, err
	}
	lenMatch, err := c.waitFor(ctx, lenRE, readyTimeout)
	if err != nil {
		return Result{}, fmt.Errorf("lish: 校验脚本长度失败: %w", err)
	}
	lenParts := lenRE.FindSubmatch(lenMatch)
	actual, aErr := strconv.Atoi(string(lenParts[1]))
	expected, eErr := strconv.Atoi(string(lenParts[2]))
	if aErr != nil || eErr != nil || actual != expected {
		return Result{}, fmt.Errorf("lish: 脚本传输不完整（期望 %d 字节，实际 %d 字节）", len(script), actual)
	}

	// 5. Run it and capture the exit code through a unique sentinel.
	marker := "__PANEL_BOOT_" + nonce + "_RC__"
	rcRE := regexp.MustCompile(regexp.QuoteMeta(marker) + `:(\d+)__`)
	start := c.mark()
	if err := c.send(ctx, fmt.Sprintf("bash '%s'; printf '\\n%s:%%d__\\n' $?\r", scriptPath, marker)); err != nil {
		return Result{}, err
	}
	match, err := c.waitFor(ctx, rcRE, scriptTimeout)
	if err != nil {
		return Result{}, fmt.Errorf("lish: 等待脚本结束标记失败: %w", err)
	}
	exit, convErr := strconv.Atoi(string(rcRE.FindSubmatch(match)[1]))
	if convErr != nil {
		return Result{}, fmt.Errorf("lish: 解析退出码失败: %w", convErr)
	}

	// 6. Best-effort cleanup.
	c.mark()
	_ = c.send(ctx, "rm -f '"+b64Path+"' '"+scriptPath+"'\r")

	return Result{
		Stdout: stripANSI(string(c.buf[start:])),
		Exit:   exit,
	}, nil
}

func stripANSI(s string) string {
	return strings.ReplaceAll(ansiRE.ReplaceAllString(s, ""), "\r", "")
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// IsConnectError reports whether err happened while opening the console
// (websocket dial/handshake), which makes a retry safe.
func IsConnectError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "lish: dial") || strings.Contains(msg, "lish: handshake")
}

// IsAuthError reports whether the console login was rejected. Retrying with
// the same password cannot help.
func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "lish: auth:")
}
