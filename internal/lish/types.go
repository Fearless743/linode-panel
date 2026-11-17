// Package lish runs boot scripts on a Linode through its Lish / Weblish
// serial console over WebSocket, instead of over SSH.
//
// Contract (frozen):
//
//	func Run(ctx context.Context, opts Options, script string) (Result, error)
//
// Run obtains one-time Weblish credentials through the Linode API, opens the
// console, logs in as root with the instance password, runs the script and
// reports its exit code via a sentinel marker printed by the remote shell.
// A nil error means the console session completed, including non-zero exit
// codes (check Result.Exit); a non-nil error means a transport, auth or
// session failure.
//
// Unlike SSH, Lish does not need a public IP, an open port 22 or password
// auth on sshd — it uses Linode's out-of-band console.
package lish

import (
	"context"
	"time"

	"linode-panel/internal/linode"
)

// Options configures one Lish script execution.
type Options struct {
	InstanceID int           // Linode instance id (required)
	PAT        string        // Linode API personal access token (required unless API is set)
	Password   string        // instance root password, typed into the console login
	APIBase    string        // Linode API v4 base URL ("" = official endpoint)
	Timeout    time.Duration // overall deadline; 0 → 5m

	// API overrides the weblish credential lookup. Tests set this; production
	// leaves it nil to use a linode.Client built from PAT/APIBase.
	API func(ctx context.Context, instanceID int) (linode.LishData, error)

	// Open overrides the websocket dial. Tests set this; production leaves it
	// nil to connect to data.WeblishURL with data.WSProtocols.
	Open func(ctx context.Context, url string, protocols []string) (Conn, error)
}

// Result carries the outcome of one Lish script execution. Stdout holds the
// combined console transcript of the script (stdout and stderr are merged by
// the serial console); Stderr is kept for parity with sshrun.Result.
type Result struct {
	Command string // human-readable description of the console session
	Stdout  string
	Stderr  string
	Exit    int    // remote script exit code; -1 if it could not be determined
	Host    string // host of the weblish endpoint, e.g. "lish.linode.com"
}

// Conn is the minimal websocket surface the console driver needs. It is
// injectable so tests can drive a scripted console without a network.
type Conn interface {
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, data []byte) error
	Close() error
}
