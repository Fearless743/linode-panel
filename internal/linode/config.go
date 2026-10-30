package linode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// Config is the subset of a Linode config profile used by the panel.
// Only the fields read/written here are declared.
type Config struct {
	ID int `json:"id"`
}

// configList is one page of GET /linode/instances/{id}/configs.
type configList struct {
	Data []Config `json:"data"`
}

// GetConfigID returns the id of the instance's first config profile.
// Linode instances always have exactly one config profile.
func (c *Client) GetConfigID(ctx context.Context, id int) (int, error) {
	var list configList
	err := c.do(ctx, http.MethodGet, "/linode/instances/"+strconv.Itoa(id)+"/configs", nil, &list)
	if err != nil {
		return 0, err
	}
	if len(list.Data) == 0 {
		return 0, fmt.Errorf("linode api: instance %d has no config profile", id)
	}
	return list.Data[0].ID, nil
}

// UpdateConfigNetworkHelper enables or disables the network helper on a
// config profile (PATCH /linode/instances/{id}/configs/{configId}).
// The instance must be offline for the change to take effect.
func (c *Client) UpdateConfigNetworkHelper(ctx context.Context, id, configID int, enabled bool) error {
	body, err := json.Marshal(map[string]any{
		"helpers": map[string]bool{"network": enabled},
	})
	if err != nil {
		return fmt.Errorf("linode api: %w", err)
	}
	return c.do(ctx, http.MethodPatch, "/linode/instances/"+strconv.Itoa(id)+"/configs/"+strconv.Itoa(configID), bytes.NewReader(body), nil)
}

// AssignIP attaches an existing reserved (or shared) IP address to a Linode
// instance (POST /networking/ips/assign). The instance must be offline.
// Assignments require the target address to already exist in the account pool.
func (c *Client) AssignIP(ctx context.Context, region, address string, linodeID int) error {
	body, err := json.Marshal(map[string]any{
		"region": region,
		"assignments": []map[string]any{
			{"address": address, "linode_id": linodeID},
		},
	})
	if err != nil {
		return fmt.Errorf("linode api: %w", err)
	}
	return c.do(ctx, http.MethodPost, "/networking/ips/assign", bytes.NewReader(body), nil)
}
