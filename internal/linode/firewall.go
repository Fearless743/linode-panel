package linode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// AllowAllFirewallTag marks the panel's allow-all Cloud Firewall so it can be
// found (and reused) across create/rebuild operations. It is applied as a
// firewall tag. Unlike labels, tags need not be unique across the account.
const AllowAllFirewallTag = "linode-panel-allow-all"

// AllowAllFirewallLabel is the label prefix used for the panel's allow-all
// Cloud Firewall. Cloud Firewall labels must be unique across the whole
// account, so the actual label is suffixed with the region (see
// allowAllFirewallLabel): a bare constant label makes the second region fail
// with HTTP 400 "Label must be unique among your Cloud Firewalls".
//
// The prefix is deliberately short (and the full label clamped to 32
// characters) because the API also enforces a 3-32 character limit and long
// region ids such as "us-southeast" would overflow a longer prefix.
const AllowAllFirewallLabel = "linode-panel-fw"

// legacyAllowAllLabel is the label older panel versions used (region-less, or
// suffixed with the region). It is still recognised on lookup so existing
// firewalls are reused rather than duplicated.
const legacyAllowAllLabel = "linode-panel-allow-all"

// allowAllFirewallLabel returns the account-unique label for the allow-all
// firewall in region. It stays within the API's 32-character limit and remains
// unique even for hypothetical over-long region ids by hashing the region tail.
func allowAllFirewallLabel(region string) string {
	label := AllowAllFirewallLabel + "-" + region
	if len(label) <= 32 {
		return label
	}
	sum := sha256.Sum256([]byte(region))
	return fmt.Sprintf("%s-%x", AllowAllFirewallLabel, sum[:4])
}

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
// InboundPolicy/OutboundPolicy are required by the API when creating or
// updating rules; they define the default action for unmatched traffic.
type FirewallRules struct {
	InboundPolicy  string         `json:"inbound_policy,omitempty"`
	OutboundPolicy string         `json:"outbound_policy,omitempty"`
	Inbound        []FirewallRule `json:"inbound,omitempty"`
	Outbound       []FirewallRule `json:"outbound,omitempty"`
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
		InboundPolicy:  "ACCEPT",
		OutboundPolicy: "ACCEPT",
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
		"label":  allowAllFirewallLabel(region),
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
// (POST /networking/firewalls/{firewallId}/devices). The API takes the entity
// as {"id": <linode id>, "type": "linode"} and requires both fields.
func (c *Client) AttachFirewall(ctx context.Context, firewallID, linodeID int) error {
	body, err := json.Marshal(map[string]any{"id": linodeID, "type": "linode"})
	if err != nil {
		return fmt.Errorf("linode api: %w", err)
	}
	return c.do(ctx, http.MethodPost, "/networking/firewalls/"+strconv.Itoa(firewallID)+"/devices", bytes.NewReader(body), nil)
}

// EnsureAllowAllFirewall returns the id of the account's allow-all firewall in
// region, creating it when none exists yet.
//
// Firewall labels are account-unique while tags are not, so the lookup matches
// the region's tagged firewall first and then adopts a firewall in region that
// already carries the region-scoped label (tags may have been stripped) or the
// legacy region-less label (created by an older panel version).
func (c *Client) EnsureAllowAllFirewall(ctx context.Context, region string) (int, error) {
	want := allowAllFirewallLabel(region)
	fws, err := c.ListFirewalls(ctx)
	if err != nil {
		return 0, err
	}
	if id, ok := findAllowAllFirewall(fws, region, want); ok {
		return id, nil
	}
	created, err := c.CreateFirewall(ctx, region)
	if err != nil {
		// A concurrent create (or a firewall with the same label appearing
		// meanwhile) may have won the race; re-list and reuse it instead of
		// failing the whole rebuild.
		if isDuplicateLabel(err) {
			if fws, listErr := c.ListFirewalls(ctx); listErr == nil {
				if id, ok := findAllowAllFirewall(fws, region, want); ok {
					return id, nil
				}
			}
		}
		return 0, err
	}
	return created.ID, nil
}

// findAllowAllFirewall locates the panel's allow-all firewall for region in
// fws: same region and either tagged, or labelled with the region-scoped label
// (current or legacy form).
func findAllowAllFirewall(fws []Firewall, region, want string) (int, bool) {
	for _, fw := range fws {
		if fw.Region != region {
			continue
		}
		if hasTag(fw.Tags, AllowAllFirewallTag) || fw.Label == want || isLegacyAllowAllLabel(fw.Label, region) {
			return fw.ID, true
		}
	}
	return 0, false
}

// isLegacyAllowAllLabel reports whether label is one older panel versions may
// have created: the bare legacy label or the legacy label suffixed with region.
func isLegacyAllowAllLabel(label, region string) bool {
	return label == legacyAllowAllLabel || label == legacyAllowAllLabel+"-"+region
}

// isDuplicateLabel reports whether err is the Linode API's 400 response for an
// already-used firewall label.
func isDuplicateLabel(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == http.StatusBadRequest &&
		strings.Contains(apiErr.Reason, "unique")
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
