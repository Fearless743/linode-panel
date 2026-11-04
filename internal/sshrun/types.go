// Package sshrun runs shell scripts on a remote host over SSH
// (golang.org/x/crypto/ssh). Used to execute the boot script on a freshly
// created Linode, since Linode has no cloud-init/user-data equivalent.
//
// Contract (frozen):
//
//	func Run(ctx context.Context, opts Options, script string) (Result, error)
//
// Run semantics: the script is piped to the remote shell's stdin
// ("bash -s"), avoiding argv length/quoting limits. Run returns a nil error
// whenever the SSH session completed, including non-zero remote exit codes
// (check Result.Exit); a non-nil error means transport/auth/session failure.
package sshrun

import "time"

// Options configures one SSH command execution.
type Options struct {
	Host               string        // IP or hostname (required)
	Port               int           // 0 → 22
	User               string        // "" → root
	Password           string        // root password for password auth
	InsecureSkipVerify bool          // accept unknown host keys (freshly provisioned hosts)
	Timeout            time.Duration // overall dial+exec deadline; 0 → 5m
}

// Result carries the outcome of one script execution.
type Result struct {
	Command string // human-readable equivalent, e.g. `ssh -p 22 root@1.2.3.4 'bash -s'`
	Stdout  string
	Stderr  string
	Exit    int // remote exit code; -1 if the session ended before a remote exit
}
