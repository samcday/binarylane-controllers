package nodecontroller

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/samcday/binarylane-controller/binarylane"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	uninitializedTaint = "node.cloudprovider.kubernetes.io/uninitialized"
	defaultInterval    = 30 * time.Second
)

type Controller struct {
	bl  *binarylane.Client
	k8s kubernetes.Interface
}

func New(bl *binarylane.Client, k8s kubernetes.Interface) *Controller {
	return &Controller{bl: bl, k8s: k8s}
}

func (c *Controller) Run(ctx context.Context) {
	slog.Info("node controller starting", "interval", defaultInterval)
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
	nodes, err := c.k8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		slog.Error("listing nodes", "error", err)
		return
	}
	for i := range nodes.Items {
		node := &nodes.Items[i]
		serverID, ok := binarylane.ParseProviderID(node.Spec.ProviderID)
		if !ok {
			continue
		}
		if err := c.reconcileNode(ctx, node, serverID); err != nil {
			slog.Error("reconciling node", "node", node.Name, "serverID", serverID, "error", err)
		}
	}
}

func (c *Controller) reconcileNode(ctx context.Context, node *corev1.Node, serverID int) error {
	server, err := c.bl.GetServer(ctx, serverID)
	if err != nil {
		return fmt.Errorf("getting server: %w", err)
	}

	if server == nil {
		slog.Info("server deleted, removing node", "node", node.Name, "serverID", serverID)
		if err := c.k8s.CoreV1().Nodes().Delete(ctx, node.Name, metav1.DeleteOptions{}); err != nil {
			return fmt.Errorf("deleting node: %w", err)
		}
		return nil
	}

	needsUpdate := false
	updated := node.DeepCopy()

	// Reconcile addresses
	desiredAddresses := serverAddresses(server)
	if !addressesEqual(node.Status.Addresses, desiredAddresses) {
		updated.Status.Addresses = desiredAddresses
		needsUpdate = true
	}

	// Reconcile labels
	for k, v := range map[string]string{
		"node.kubernetes.io/instance-type":  server.SizeSlug,
		"topology.kubernetes.io/region":     server.Region.Slug,
		"node.kubernetes.io/cloud-provider": binarylane.ProviderName,
	} {
		if updated.Labels == nil {
			updated.Labels = make(map[string]string)
		}
		if updated.Labels[k] != v {
			updated.Labels[k] = v
			needsUpdate = true
		}
	}

	// Remove uninitialized taint
	taintRemoved := false
	var newTaints []corev1.Taint
	for _, t := range updated.Spec.Taints {
		if t.Key == uninitializedTaint {
			taintRemoved = true
			continue
		}
		newTaints = append(newTaints, t)
	}
	if taintRemoved {
		updated.Spec.Taints = newTaints
		slog.Info("removing uninitialized taint", "node", node.Name)
	}

	if !needsUpdate && !taintRemoved {
		return nil
	}

	if _, err := c.k8s.CoreV1().Nodes().Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating node: %w", err)
	}
	if !addressesEqual(node.Status.Addresses, desiredAddresses) {
		if _, err := c.k8s.CoreV1().Nodes().UpdateStatus(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("updating node status: %w", err)
		}
	}

	slog.Info("reconciled node", "node", node.Name, "serverID", serverID)
	return nil
}

func serverAddresses(server *binarylane.Server) []corev1.NodeAddress {
	var addrs []corev1.NodeAddress
	for _, net := range server.Networks.V4 {
		switch net.Type {
		case "public":
			addrs = append(addrs, corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: net.IPAddress})
		case "private":
			addrs = append(addrs, corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: net.IPAddress})
		}
	}
	addrs = append(addrs, corev1.NodeAddress{Type: corev1.NodeHostName, Address: server.Name})
	return addrs
}

func addressesEqual(a, b []corev1.NodeAddress) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]string)
	for _, addr := range a {
		m[string(addr.Type)] = addr.Address
	}
	for _, addr := range b {
		if m[string(addr.Type)] != addr.Address {
			return false
		}
	}
	return true
}
