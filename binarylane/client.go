package binarylane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const DefaultAPIBase = "https://api.binarylane.com.au/v2"

type Server struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	SizeSlug  string    `json:"size_slug"`
	Region    Region    `json:"region"`
	Image     Image     `json:"image"`
	CreatedAt time.Time `json:"created_at"`
	Networks  Networks  `json:"networks"`
	VPCID     *int      `json:"vpc_id"`
}

type Region struct {
	Slug string `json:"slug"`
}

type Image struct {
	Slug string `json:"slug"`
}

type Networks struct {
	V4 []NetworkV4 `json:"v4"`
}

type NetworkV4 struct {
	IPAddress string `json:"ip_address"`
	Type      string `json:"type"` // "public" or "private"
}

type CreateServerRequest struct {
	Name     string   `json:"name"`
	Size     string   `json:"size"`
	Image    string   `json:"image"`
	Region   string   `json:"region"`
	UserData string   `json:"user_data,omitempty"`
	SSHKeys  []string `json:"ssh_keys,omitempty"`
}

type Client struct {
	token   string
	apiBase string
	client  *http.Client
}

func NewClient(token string) *Client {
	return &Client{
		token:   token,
		apiBase: DefaultAPIBase,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiBase+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	return c.client.Do(req)
}

func (c *Client) GetServer(ctx context.Context, serverID int) (*Server, error) {
	resp, err := c.do(ctx, "GET", fmt.Sprintf("/servers/%d", serverID), nil)
	if err != nil {
		return nil, fmt.Errorf("getting server %d: %w", serverID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, nil
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getting server %d: %s: %s", serverID, resp.Status, b)
	}
	var result struct {
		Server Server `json:"server"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding server %d: %w", serverID, err)
	}
	return &result.Server, nil
}

func (c *Client) ListServers(ctx context.Context) ([]Server, error) {
	var all []Server
	page := 1
	for {
		resp, err := c.do(ctx, "GET", fmt.Sprintf("/servers?page=%d&per_page=100", page), nil)
		if err != nil {
			return nil, fmt.Errorf("listing servers: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("listing servers: %s: %s", resp.Status, b)
		}
		var result struct {
			Servers []Server `json:"servers"`
			Links   struct {
				Pages struct {
					Next string `json:"next"`
				} `json:"pages"`
			} `json:"links"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, fmt.Errorf("decoding servers: %w", err)
		}
		all = append(all, result.Servers...)
		if result.Links.Pages.Next == "" {
			break
		}
		page++
	}
	return all, nil
}

func (c *Client) CreateServer(ctx context.Context, req CreateServerRequest) (*Server, error) {
	resp, err := c.do(ctx, "POST", "/servers", req)
	if err != nil {
		return nil, fmt.Errorf("creating server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 && resp.StatusCode != 202 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("creating server: %s: %s", resp.Status, b)
	}
	var result struct {
		Server Server `json:"server"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding server: %w", err)
	}
	return &result.Server, nil
}

func (c *Client) DeleteServer(ctx context.Context, serverID int) error {
	resp, err := c.do(ctx, "DELETE", fmt.Sprintf("/servers/%d", serverID), nil)
	if err != nil {
		return fmt.Errorf("deleting server %d: %w", serverID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("deleting server %d: %s: %s", serverID, resp.Status, b)
	}
	return nil
}

const ProviderName = "binarylane"

func ServerProviderID(serverID int) string {
	return fmt.Sprintf("%s:///%d", ProviderName, serverID)
}

func ParseProviderID(pid string) (int, bool) {
	prefix := ProviderName + ":///"
	if len(pid) <= len(prefix) || pid[:len(prefix)] != prefix {
		return 0, false
	}
	var id int
	for _, c := range pid[len(prefix):] {
		if c < '0' || c > '9' {
			return 0, false
		}
		id = id*10 + int(c-'0')
	}
	return id, true
}
