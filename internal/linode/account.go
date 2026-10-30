package linode

import (
	"context"
	"fmt"
	"net/http"
)

// GetAccount returns the account associated with the client token.
// It mirrors GET /v4/account and lets the panel detect an out-of-balance
// (Balance > 0) or suspended account before rebuilding an instance on it.
func (c *Client) GetAccount(ctx context.Context) (Account, error) {
	var acct Account
	if err := c.do(ctx, http.MethodGet, "/account", nil, &acct); err != nil {
		return Account{}, fmt.Errorf("linode api: get account: %w", err)
	}
	return acct, nil
}
