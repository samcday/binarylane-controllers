package binarylane

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

type LoadBalancer struct {
	ID              int                    `json:"id"`
	Name            string                 `json:"name"`
	IP              string                 `json:"ip"`
	Status          string                 `json:"status"`
	ForwardingRules []ForwardingRule       `json:"forwarding_rules"`
	HealthCheck     *HealthCheck           `json:"health_check,omitempty"`
	ServerIDs       []int                  `json:"server_ids"`
	Region          Region                 `json:"region"`
}

type ForwardingRule struct {
	EntryProtocol  string `json:"entry_protocol"`
	EntryPort      int    `json:"entry_port"`
	TargetProtocol string `json:"target_protocol"`
	TargetPort     int    `json:"target_port"`
}

type HealthCheck struct {
	Protocol               string `json:"protocol"`
	Port                   int    `json:"port"`
	Path                   string `json:"path,omitempty"`
	CheckIntervalSeconds   int    `json:"check_interval_seconds"`
	ResponseTimeoutSeconds int    `json:"response_timeout_seconds"`
	UnhealthyThreshold     int    `json:"unhealthy_threshold"`
	HealthyThreshold       int    `json:"healthy_threshold"`
}

type CreateLoadBalancerRequest struct {
	Name            string           `json:"name"`
	Region          string           `json:"region"`
	ForwardingRules []ForwardingRule `json:"forwarding_rules"`
	HealthCheck     *HealthCheck     `json:"health_check,omitempty"`
	ServerIDs       []int            `json:"server_ids"`
}

type UpdateLoadBalancerRequest struct {
	Name            string           `json:"name"`
	ForwardingRules []ForwardingRule `json:"forwarding_rules"`
	HealthCheck     *HealthCheck     `json:"health_check,omitempty"`
	ServerIDs       []int            `json:"server_ids"`
}

func (c *Client) GetLoadBalancer(ctx context.Context, lbID int) (*LoadBalancer, error) {
	resp, err := c.do(ctx, "GET", fmt.Sprintf("/load_balancers/%d", lbID), nil)
	if err != nil {
		return nil, fmt.Errorf("getting load balancer %d: %w", lbID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, nil
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getting load balancer %d: %s: %s", lbID, resp.Status, b)
	}
	var result struct {
		LoadBalancer LoadBalancer `json:"load_balancer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding load balancer %d: %w", lbID, err)
	}
	return &result.LoadBalancer, nil
}

func (c *Client) CreateLoadBalancer(ctx context.Context, req CreateLoadBalancerRequest) (*LoadBalancer, error) {
	resp, err := c.do(ctx, "POST", "/load_balancers", req)
	if err != nil {
		return nil, fmt.Errorf("creating load balancer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 && resp.StatusCode != 202 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("creating load balancer: %s: %s", resp.Status, b)
	}
	var result struct {
		LoadBalancer LoadBalancer `json:"load_balancer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding load balancer: %w", err)
	}
	return &result.LoadBalancer, nil
}

func (c *Client) UpdateLoadBalancer(ctx context.Context, lbID int, req UpdateLoadBalancerRequest) (*LoadBalancer, error) {
	resp, err := c.do(ctx, "PUT", fmt.Sprintf("/load_balancers/%d", lbID), req)
	if err != nil {
		return nil, fmt.Errorf("updating load balancer %d: %w", lbID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("updating load balancer %d: %s: %s", lbID, resp.Status, b)
	}
	var result struct {
		LoadBalancer LoadBalancer `json:"load_balancer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding load balancer %d: %w", lbID, err)
	}
	return &result.LoadBalancer, nil
}

func (c *Client) DeleteLoadBalancer(ctx context.Context, lbID int) error {
	resp, err := c.do(ctx, "DELETE", fmt.Sprintf("/load_balancers/%d", lbID), nil)
	if err != nil {
		return fmt.Errorf("deleting load balancer %d: %w", lbID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("deleting load balancer %d: %s: %s", lbID, resp.Status, b)
	}
	return nil
}
