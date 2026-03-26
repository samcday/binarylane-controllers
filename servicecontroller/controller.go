package servicecontroller

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/samcday/binarylane-controller/binarylane"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	defaultInterval = 30 * time.Second

	// Annotation keys
	annotationLBID     = "binarylane.com.au/load-balancer-id"
	annotationLBRegion = "binarylane.com.au/load-balancer-region"

	finalizerName = "binarylane.com.au/load-balancer"
)

type Controller struct {
	bl  *binarylane.Client
	k8s kubernetes.Interface
}

func New(bl *binarylane.Client, k8s kubernetes.Interface) *Controller {
	return &Controller{bl: bl, k8s: k8s}
}

func (c *Controller) Run(ctx context.Context) {
	slog.Info("service controller starting", "interval", defaultInterval)
	c.reconcile(ctx)
	ticker := time.NewTicker(defaultInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.reconcile(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (c *Controller) reconcile(ctx context.Context) {
	services, err := c.k8s.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		slog.Error("listing services", "error", err)
		return
	}
	for i := range services.Items {
		svc := &services.Items[i]
		if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
			continue
		}
		if err := c.reconcileService(ctx, svc); err != nil {
			slog.Error("reconciling service", "service", svc.Namespace+"/"+svc.Name, "error", err)
		}
	}
}

func (c *Controller) reconcileService(ctx context.Context, svc *corev1.Service) error {
	// Handle deletion
	if svc.DeletionTimestamp != nil {
		return c.handleDeletion(ctx, svc)
	}

	// Ensure finalizer
	if !hasFinalizer(svc) {
		updated := svc.DeepCopy()
		updated.Finalizers = append(updated.Finalizers, finalizerName)
		if _, err := c.k8s.CoreV1().Services(svc.Namespace).Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("adding finalizer: %w", err)
		}
	}

	// Collect target node IPs for the LB backend
	nodeIDs, err := c.getReadyNodeServerIDs(ctx)
	if err != nil {
		return fmt.Errorf("getting node server IDs: %w", err)
	}

	// Build forwarding rules from service ports
	var rules []binarylane.ForwardingRule
	for _, port := range svc.Spec.Ports {
		proto := strings.ToLower(string(port.Protocol))
		if proto == "" {
			proto = "tcp"
		}
		rules = append(rules, binarylane.ForwardingRule{
			EntryProtocol:  proto,
			EntryPort:      int(port.Port),
			TargetProtocol: proto,
			TargetPort:     int(port.NodePort),
		})
	}

	healthCheck := &binarylane.HealthCheck{
		Protocol:               "tcp",
		Port:                   int(svc.Spec.Ports[0].NodePort),
		CheckIntervalSeconds:   10,
		ResponseTimeoutSeconds: 5,
		UnhealthyThreshold:     3,
		HealthyThreshold:       5,
	}

	lbIDStr := svc.Annotations[annotationLBID]
	if lbIDStr != "" {
		// Update existing LB
		lbID, err := strconv.Atoi(lbIDStr)
		if err != nil {
			return fmt.Errorf("parsing LB ID annotation: %w", err)
		}
		existing, err := c.bl.GetLoadBalancer(ctx, lbID)
		if err != nil {
			return fmt.Errorf("getting load balancer: %w", err)
		}
		if existing == nil {
			// LB was deleted externally, recreate
			slog.Warn("load balancer not found, recreating", "lbID", lbID, "service", svc.Namespace+"/"+svc.Name)
			return c.createLoadBalancer(ctx, svc, rules, healthCheck, nodeIDs)
		}
		lb, err := c.bl.UpdateLoadBalancer(ctx, lbID, binarylane.UpdateLoadBalancerRequest{
			Name:            lbName(svc),
			ForwardingRules: rules,
			HealthCheck:     healthCheck,
			ServerIDs:       nodeIDs,
		})
		if err != nil {
			return fmt.Errorf("updating load balancer: %w", err)
		}
		return c.updateServiceStatus(ctx, svc, lb)
	}

	// Create new LB
	return c.createLoadBalancer(ctx, svc, rules, healthCheck, nodeIDs)
}

func (c *Controller) createLoadBalancer(ctx context.Context, svc *corev1.Service, rules []binarylane.ForwardingRule, healthCheck *binarylane.HealthCheck, nodeIDs []int) error {
	region := svc.Annotations[annotationLBRegion]
	if region == "" {
		region = "syd"
	}

	slog.Info("creating load balancer", "service", svc.Namespace+"/"+svc.Name, "region", region)
	lb, err := c.bl.CreateLoadBalancer(ctx, binarylane.CreateLoadBalancerRequest{
		Name:            lbName(svc),
		Region:          region,
		ForwardingRules: rules,
		HealthCheck:     healthCheck,
		ServerIDs:       nodeIDs,
	})
	if err != nil {
		return fmt.Errorf("creating load balancer: %w", err)
	}

	// Set the LB ID annotation
	updated := svc.DeepCopy()
	if updated.Annotations == nil {
		updated.Annotations = make(map[string]string)
	}
	updated.Annotations[annotationLBID] = strconv.Itoa(lb.ID)
	if _, err := c.k8s.CoreV1().Services(svc.Namespace).Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating service annotation: %w", err)
	}

	return c.updateServiceStatus(ctx, svc, lb)
}

func (c *Controller) updateServiceStatus(ctx context.Context, svc *corev1.Service, lb *binarylane.LoadBalancer) error {
	if lb.IP == "" {
		return nil
	}
	desired := []corev1.LoadBalancerIngress{{IP: lb.IP}}
	if ingressEqual(svc.Status.LoadBalancer.Ingress, desired) {
		return nil
	}
	updated := svc.DeepCopy()
	updated.Status.LoadBalancer.Ingress = desired
	if _, err := c.k8s.CoreV1().Services(svc.Namespace).UpdateStatus(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating service status: %w", err)
	}
	slog.Info("updated service ingress", "service", svc.Namespace+"/"+svc.Name, "ip", lb.IP)
	return nil
}

func (c *Controller) handleDeletion(ctx context.Context, svc *corev1.Service) error {
	if !hasFinalizer(svc) {
		return nil
	}
	if lbIDStr := svc.Annotations[annotationLBID]; lbIDStr != "" {
		lbID, err := strconv.Atoi(lbIDStr)
		if err != nil {
			return fmt.Errorf("parsing LB ID for deletion: %w", err)
		}
		slog.Info("deleting load balancer", "lbID", lbID, "service", svc.Namespace+"/"+svc.Name)
		if err := c.bl.DeleteLoadBalancer(ctx, lbID); err != nil {
			return fmt.Errorf("deleting load balancer: %w", err)
		}
	}
	// Remove finalizer
	updated := svc.DeepCopy()
	var finalizers []string
	for _, f := range updated.Finalizers {
		if f != finalizerName {
			finalizers = append(finalizers, f)
		}
	}
	updated.Finalizers = finalizers
	if _, err := c.k8s.CoreV1().Services(svc.Namespace).Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("removing finalizer: %w", err)
	}
	return nil
}

func (c *Controller) getReadyNodeServerIDs(ctx context.Context) ([]int, error) {
	nodes, err := c.k8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var ids []int
	for _, node := range nodes.Items {
		serverID, ok := binarylane.ParseProviderID(node.Spec.ProviderID)
		if !ok {
			continue
		}
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				ids = append(ids, serverID)
				break
			}
		}
	}
	return ids, nil
}

func lbName(svc *corev1.Service) string {
	return fmt.Sprintf("k8s-%s-%s", svc.Namespace, svc.Name)
}

func hasFinalizer(svc *corev1.Service) bool {
	for _, f := range svc.Finalizers {
		if f == finalizerName {
			return true
		}
	}
	return false
}

func ingressEqual(a, b []corev1.LoadBalancerIngress) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].IP != b[i].IP {
			return false
		}
	}
	return true
}
