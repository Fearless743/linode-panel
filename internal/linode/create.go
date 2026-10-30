package linode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// CreateInstance deploys a new Linode from an image (POST /linode/instances).
// opts.Region, opts.Type and opts.Image are required; authentication needs
// RootPass or AuthorizedKeys (see CreateOptions).
func (c *Client) CreateInstance(ctx context.Context, opts CreateOptions) (Instance, error) {
	if opts.Region == "" || opts.Type == "" || opts.Image == "" {
		return Instance{}, fmt.Errorf("linode api: region, type and image are required")
	}
	body, err := json.Marshal(opts)
	if err != nil {
		return Instance{}, fmt.Errorf("linode api: %w", err)
	}
	var inst Instance
	err = c.do(ctx, http.MethodPost, "/linode/instances", bytes.NewReader(body), &inst)
	return inst, err
}

// DeleteInstance permanently removes a Linode and its disks (DELETE
// /linode/instances/{id}). Unreserved (ephemeral) IP addresses are released;
// reserved IPs return to the account pool.
func (c *Client) DeleteInstance(ctx context.Context, id int) error {
	return c.do(ctx, http.MethodDelete, "/linode/instances/"+strconv.Itoa(id), nil, nil)
}
