package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// panelClient speaks the frozen agent protocol against the panel.
type panelClient struct {
	base  string
	token string
	http  *http.Client
}

func newPanelClient(base, token string) *panelClient {
	return &panelClient{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

// registerResponse mirrors POST /api/agents/register.
type registerResponse struct {
	AgentID int    `json:"agent_id"`
	Token   string `json:"agent_token"`
}

// errUnauthorized marks a 401/403 panel response (invalid token/code).
var errUnauthorized = errors.New("panel 拒绝身份")

// register exchanges the registration code for an agent identity.
func (c *panelClient) register(ctx context.Context, code, name string) (registerResponse, error) {
	var out registerResponse
	err := c.doJSON(ctx, http.MethodPost, "/api/agents/register", map[string]string{
		"registration_code": code,
		"name":              name,
	}, &out)
	return out, err
}

// pollCommands is the long-poll body for POST /api/agent/poll.
func (c *panelClient) poll(ctx context.Context, hostname string) ([]pollCommand, error) {
	var out struct {
		Commands []pollCommand `json:"commands"`
	}
	body := map[string]string{"hostname": hostname}
	err := c.doJSON(ctx, http.MethodPost, "/api/agent/poll", body, &out)
	return out.Commands, err
}

// pollCommand is one command returned by the panel. Type is a Cmd* constant.
type pollCommand struct {
	ID     int64          `json:"id"`
	Type   string         `json:"type"`
	Params map[string]any `json:"params"`
}

// reportResult posts the final (or progress) result for a command.
func (c *panelClient) reportResult(ctx context.Context, cmdID int64, ok bool, errMsg string, payload map[string]any) error {
	body := map[string]any{
		"command_id": cmdID,
		"ok":         ok,
	}
	if errMsg != "" {
		body["error"] = errMsg
	}
	if payload != nil {
		body["payload"] = payload
	}
	return c.doJSON(ctx, http.MethodPost, "/api/agent/result", body, nil)
}

// doJSON performs one panel request. 401/403 maps to errUnauthorized.
func (c *panelClient) doJSON(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("序列化请求失败: %w", err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("请求面板失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("读取面板响应失败: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("面板返回状态 %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("解析面板响应失败: %w", err)
		}
	}
	return nil
}

// ensureToken registers if needed, persisting the token to state. When the
// stored token is present it is reused.
func ensureToken(ctx context.Context, st *state, path string, pc *panelClient, code, hostname string) error {
	if st.AgentToken != "" {
		pc.token = st.AgentToken
		return nil
	}
	if code == "" {
		return errors.New("缺少注册码：首次运行必须用 --code 注册 agent")
	}
	resp, err := pc.register(ctx, code, hostname)
	if err != nil {
		return fmt.Errorf("注册 agent 失败: %w", err)
	}
	if resp.Token == "" {
		return errors.New("注册 agent 失败: 面板未返回 token")
	}
	st.AgentID = resp.AgentID
	st.AgentToken = resp.Token
	pc.token = resp.Token
	return saveState(path, *st)
}
