package linode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// AllowAllFirewallTag marks the panel's allow-all Cloud Firewall so it can be
// found (and reused) across create/rebuild operations. It is applied as a
// firewall tag.
const AllowAllFirewallTag = "linode-panel-allow-all"

// AllowAllFirewallLabel is the display label used when creating the firewall.
const AllowAllFirewallLabel = "linode-panel-allow-all"

// FirewallAddresses restricts a rule to source (inbound) or destination
// (outbound) IP ranges. An empty slice means "any" for that address family.
type FirewallAddresses struct {
	IPv4 []string `json:"ipv4,omitempty"`
	IPv6 []string `json:"ipv6,omitempty"`
}

// FirewallRule is one allow/deny rule of a Cloud Firewall.
type FirewallRule struct {
	Label     string            `json:"label,omitempty"`
	Action    string            `json:"action"` // "ACCEPT" or "DROP"
	Protocol  string            `json:"protocol"`
	Ports     string            `json:"ports,omitempty"` // e.g. "1-65535"; omitted for ICMP
	Addresses FirewallAddresses `json:"addresses,omitempty"`
}

// FirewallRules is the inbound/outbound rule set of a Cloud Firewall.
type FirewallRules struct {
	Inbound  []FirewallRule `json:"inbound,omitempty"`
	Outbound []FirewallRule `json:"outbound,omitempty"`
}

// Firewall is the subset of a Cloud Firewall returned by the API.
type Firewall struct {
	ID     int           `json:"id"`
	Label  string        `json:"label"`
	Region string        `json:"region"`
	Status string        `json:"status"`
	Tags   []string      `json:"tags"`
	Rules  FirewallRules `json:"rules"`
}

// FirewallEntity identifies the Linode a firewall device is attached to.
type FirewallEntity struct {
	ID    int    `json:"id"`
	Type  string `json:"type"`
	Label string `json:"label"`
}

// FirewallDevice is one Linode attached to a Cloud Firewall.
type FirewallDevice struct {
	ID     int            `json:"id"`
	Entity FirewallEntity `json:"entity"`
	Label  string         `json:"label"`
}

// allowAllRules returns the rule set that accepts all inbound/outbound
// TCP, UDP and ICMP traffic from/to any address.
func allowAllRules() FirewallRules {
	anyAddr := FirewallAddresses{IPv4: []string{"0.0.0.0/0"}, IPv6: []string{"::/0"}}
	anyPort := "1-65535"
	return FirewallRules{
		Inbound: []FirewallRule{
			{Label: "allow-all-in-tcp", Action: "ACCEPT", Protocol: "TCP", Ports: anyPort, Addresses: anyAddr},
			{Label: "allow-all-in-udp", Action: "ACCEPT", Protocol: "UDP", Ports: anyPort, Addresses: anyAddr},
			{Label: "allow-all-in-icmp", Action: "ACCEPT", Protocol: "ICMP", Addresses: anyAddr},
		},
		Outbound: []FirewallRule{
			{Label: "allow-all-out-tcp", Action: "ACCEPT", Protocol: "TCP", Ports: anyPort, Addresses: anyAddr},
			{Label: "allow-all-out-udp", Action: "ACCEPT", Protocol: "UDP", Ports: anyPort, Addresses: anyAddr},
			{Label: "allow-all-out-icmp", Action: "ACCEPT", Protocol: "ICMP", Addresses: anyAddr},
		},
	}
}

// ListFirewalls returns all Cloud Firewalls on the account, following pagination.
func (c *Client) ListFirewalls(ctx context.Context) ([]Firewall, error) {
	return paginate[Firewall](c, ctx, "/networking/firewalls")
}

// CreateFirewall creates a Cloud Firewall in region that accepts all traffic.
func (c *Client) CreateFirewall(ctx context.Context, region string) (Firewall, error) {
	body, err := json.Marshal(map[string]any{
		"label":  AllowAllFirewallLabel,
		"region": region,
		"rules":  allowAllRules(),
		"tags":   []string{AllowAllFirewallTag},
	})
	if err != nil {
		return Firewall{}, fmt.Errorf("linode api: %w", err)
	}
	var fw Firewall
	err = c.do(ctx, http.MethodPost, "/networking/firewalls", bytes.NewReader(body), &fw)
	return fw, err
}

// ListFirewallDevices returns the Linodes attached to a firewall.
func (c *Client) ListFirewallDevices(ctx context.Context, firewallID int) ([]FirewallDevice, error) {
	return paginate[FirewallDevice](c, ctx, "/networking/firewalls/"+strconv.Itoa(firewallID)+"/devices")
}

// AttachFirewall attaches a firewall to a Linode
// (POST /networking/firewalls/{firewallId}/devices).
func (c *Client) AttachFirewall(ctx context.Context, firewallID, linodeID int) error {
	body, err := json.Marshal(map[string]any{"linode_id": linodeID})
	if err != nil {
		return fmt.Errorf("linode api: %w", err)
	}
	return c.do(ctx, http.MethodPost, "/networking/firewalls/"+strconv.Itoa(firewallID)+"/devices", bytes.NewReader(body), nil)
}

// EnsureAllowAllFirewall returns the id of the account's allow-all firewall in
// region, creating it when none exists yet.
func (c *Client) EnsureAllowAllFirewall(ctx context.Context, region string) (int, error) {
	fws, err := c.ListFirewalls(ctx)
	if err != nil {
		return 0, err
	}
	for _, fw := range fws {
		if fw.Region == region && hasTag(fw.Tags, AllowAllFirewallTag) {
			return fw.ID, nil
		}
	}
	created, err := c.CreateFirewall(ctx, region)
	if err != nil {
		return 0, err
	}
	return created.ID, nil
}

// AttachAllowAllFirewall ensures the account's allow-all firewall exists in
// region and attaches it to the Linode. It is idempotent: when the Linode is
// already attached, it is a no-op. Callers treat failure as best-effort
// (the Linode simply keeps its current firewall policy).
func (c *Client) AttachAllowAllFirewall(ctx context.Context, region string, linodeID int) error {
	fwID, err := c.EnsureAllowAllFirewall(ctx, region)
	if err != nil {
		return err
	}
	devs, err := c.ListFirewallDevices(ctx, fwID)
	if err != nil {
		return err
	}
	for _, d := range devs {
		if d.Entity.ID == linodeID {
			return nil // already attached
		}
	}
	return c.AttachFirewall(ctx, fwID, linodeID)
}

// hasTag reports whether tags contains tag.
func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}
