// Package linode is a minimal client for the Linode API v4.
//
// Contract for the rest of the panel (frozen; see main.go / internal/api):
//
//	type Client struct{ Token, BaseURL string; HTTP *http.Client; UserAgent string }
//	func New(token string) *Client
//	func (c *Client) ListInstances(ctx context.Context) ([]Instance, error)
//	func (c *Client) GetInstance(ctx context.Context, id int) (Instance, error)
//	func (c *Client) Boot(ctx context.Context, id int) error
//	func (c *Client) Shutdown(ctx context.Context, id int) error
//	func (c *Client) Reboot(ctx context.Context, id int) error
//	func (c *Client) GetInstanceIPs(ctx context.Context, id int) (InstanceIPs, error)
//	func (c *Client) ListSSHKeys(ctx context.Context) ([]SSHKey, error)
//	func (c *Client) CreateInstance(ctx context.Context, opts CreateOptions) (Instance, error)
//	func (c *Client) DeleteInstance(ctx context.Context, id int) error
//	func (c *Client) GetConfigID(ctx context.Context, id int) (int, error)
//	func (c *Client) UpdateConfigNetworkHelper(ctx context.Context, id, configID int, enabled bool) error
//	func (c *Client) AttachAllowAllFirewall(ctx context.Context, region string, linodeID int) error
package linode

import "fmt"

// Instance is a Linode instance; subset returned by
// GET /v4/linode/instances and GET /v4/linode/instances/{id}.
type Instance struct {
	ID      int      `json:"id"`
	Label   string   `json:"label"`
	Status  string   `json:"status"`
	Region  string   `json:"region"`
	Type    string   `json:"type"`
	Image   string   `json:"image"`
	IPv4    []string `json:"ipv4"`
	IPv6    string   `json:"ipv6"`
	Tags    []string `json:"tags"`
	Created string   `json:"created"`
	Updated string   `json:"updated"`
}

// CreateOptions is the subset of POST /v4/linode/instances used by the panel.
// Requires at least one of RootPass / AuthorizedKeys for authentication.
type CreateOptions struct {
	Region         string   `json:"region"`
	Type           string   `json:"type"`
	Image          string   `json:"image"` // e.g. "linode/debian12"; deploys from this image
	RootPass       string   `json:"root_pass,omitempty"`
	AuthorizedKeys []string `json:"authorized_keys,omitempty"`
	Label          string   `json:"label,omitempty"`
	Booted         *bool    `json:"booted,omitempty"` // nil → API default (true)
	Tags           []string `json:"tags,omitempty"`
}

// IPAddress is one entry of the ipv4 / ipv6 collections of InstanceIPs.
type IPAddress struct {
	Address string `json:"address"`
	RDNS    string `json:"rdns"`
	Prefix  int    `json:"prefix"`
	Public  bool   `json:"public"`
}

// InstanceIPs mirrors GET /v4/linode/instances/{id}/ips.
// Note: collections hold objects, not plain strings.
type InstanceIPs struct {
	IPv4 struct {
		Public   []IPAddress `json:"public"`
		Private  []IPAddress `json:"private"`
		Reserved []IPAddress `json:"reserved"`
		Shared   []IPAddress `json:"shared"`
	} `json:"ipv4"`
	IPv6 struct {
		SLAAC     IPAddress `json:"slaac"`
		LinkLocal IPAddress `json:"link_local"`
	} `json:"ipv6"`
}

// PublicIPv4 returns the first public IPv4 address, or "".
func (i InstanceIPs) PublicIPv4() string {
	if len(i.IPv4.Public) > 0 {
		return i.IPv4.Public[0].Address
	}
	return ""
}

// SLAACv6 returns the SLAAC IPv6 address, or "".
func (i InstanceIPs) SLAACv6() string { return i.IPv6.SLAAC.Address }

// Account mirrors the subset of GET /v4/account used by the panel.
// Balance is the current amount owed (positive = amount due).
type Account struct {
	Balance           float64 `json:"balance"`            // 当前欠费(正数=欠款)
	BalanceUninvoiced float64 `json:"balance_uninvoiced"` // 未出账金额
	Company           string  `json:"company"`
	Email             string  `json:"email"`
}

// SSHKey mirrors an item of GET /v4/profile/sshkeys.
type SSHKey struct {
	ID      int    `json:"id"`
	Label   string `json:"label"`
	SSHKey  string `json:"ssh_key"`
	Created string `json:"created"`
}

// APIError is a non-2xx Linode API response.
// Reason comes from the first element of the "errors" array when present.
type APIError struct {
	StatusCode int
	Reason     string
}

func (e *APIError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("linode api: status %d", e.StatusCode)
	}
	return fmt.Sprintf("linode api: status %d: %s", e.StatusCode, e.Reason)
}
