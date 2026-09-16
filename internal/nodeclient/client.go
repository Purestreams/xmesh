package nodeclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"xmesh/internal/controller"
	"xmesh/internal/model"
)

type Client struct {
	ControllerURL string
	Credential    string
	HTTP          *http.Client
}

func New(controllerURL, credential string) *Client {
	return &Client{ControllerURL: strings.TrimSuffix(controllerURL, "/"), Credential: credential, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

func FetchGateway(ctx context.Context, client *Client) (controller.GatewayConfig, error) {
	var result controller.GatewayConfig
	err := client.getJSON(ctx, "/api/v1/config", &result)
	return result, err
}

func FetchAgent(ctx context.Context, client *Client) (controller.AgentConfig, error) {
	var result controller.AgentConfig
	err := client.getJSON(ctx, "/api/v1/config", &result)
	return result, err
}

func (c *Client) Report(ctx context.Context, status model.NodeStatus, links []model.LinkStatus, grants []model.GrantStatus) error {
	return c.postJSON(ctx, "/api/v1/status", controller.StatusReport{Status: status, Links: links, Grants: grants}, nil)
}

func (c *Client) getJSON(ctx context.Context, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ControllerURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Credential)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("controller request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode controller response: %w", err)
	}
	return nil
}

func (c *Client) postJSON(ctx context.Context, path string, value, target any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ControllerURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Credential)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("controller request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(resp)
	}
	if target != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(target); err != nil {
			return err
		}
	}
	return nil
}

func responseError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("controller returned %s: %s", resp.Status, strings.TrimSpace(string(b)))
}
