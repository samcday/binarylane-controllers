package autoscaler

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/samcday/binarylane-controller/binarylane"
	pb "github.com/samcday/binarylane-controller/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type NodeGroupConfig struct {
	ID       string            `json:"id"`
	MinSize  int               `json:"minSize"`
	MaxSize  int               `json:"maxSize"`
	Size     string            `json:"size"`
	Region   string            `json:"region"`
	Image    string            `json:"image"`
	VCPUs    int               `json:"vcpus"`
	MemoryMB int               `json:"memoryMB"`
	DiskGB   int               `json:"diskGB"`
	Labels   map[string]string `json:"labels,omitempty"`
}

type Config struct {
	NodeGroups   []NodeGroupConfig `json:"nodeGroups"`
	CloudInit    string            `json:"cloudInit"`
	SSHKeys      []string          `json:"sshKeys,omitempty"`
	NamePrefix   string            `json:"namePrefix"`
	TemplateVars map[string]string `json:"-"`
}

type Provider struct {
	pb.UnimplementedCloudProviderServer

	mu        sync.RWMutex
	bl        *binarylane.Client
	cfg       Config
	servers   map[int]*binarylane.Server
	cloudInit *template.Template
}

func NewProvider(bl *binarylane.Client, cfg Config) (*Provider, error) {
	tmpl, err := template.New("cloudinit").Parse(cfg.CloudInit)
	if err != nil {
		return nil, fmt.Errorf("parsing cloud-init template: %w", err)
	}
	return &Provider{
		bl:        bl,
		cfg:       cfg,
		servers:   make(map[int]*binarylane.Server),
		cloudInit: tmpl,
	}, nil
}

func (p *Provider) serversForGroup(groupID string) []*binarylane.Server {
	prefix := p.cfg.NamePrefix + groupID + "-"
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []*binarylane.Server
	for _, s := range p.servers {
		if strings.HasPrefix(s.Name, prefix) {
			out = append(out, s)
		}
	}
	return out
}

func (p *Provider) findGroup(id string) *NodeGroupConfig {
	for i := range p.cfg.NodeGroups {
		if p.cfg.NodeGroups[i].ID == id {
			return &p.cfg.NodeGroups[i]
		}
	}
	return nil
}

func (p *Provider) NodeGroups(_ context.Context, _ *pb.NodeGroupsRequest) (*pb.NodeGroupsResponse, error) {
	var groups []*pb.NodeGroup
	for _, ng := range p.cfg.NodeGroups {
		groups = append(groups, &pb.NodeGroup{
			Id:      ng.ID,
			MinSize: int32(ng.MinSize),
			MaxSize: int32(ng.MaxSize),
			Debug:   fmt.Sprintf("BinaryLane %s in %s (size: %s)", ng.ID, ng.Region, ng.Size),
		})
	}
	return &pb.NodeGroupsResponse{NodeGroups: groups}, nil
}

func (p *Provider) NodeGroupForNode(_ context.Context, req *pb.NodeGroupForNodeRequest) (*pb.NodeGroupForNodeResponse, error) {
	if req.Node == nil {
		return &pb.NodeGroupForNodeResponse{}, nil
	}
	serverID, ok := binarylane.ParseProviderID(req.Node.ProviderID)
	if !ok {
		return &pb.NodeGroupForNodeResponse{NodeGroup: &pb.NodeGroup{}}, nil
	}
	p.mu.RLock()
	srv, exists := p.servers[serverID]
	p.mu.RUnlock()
	if !exists {
		return &pb.NodeGroupForNodeResponse{NodeGroup: &pb.NodeGroup{}}, nil
	}
	for _, ng := range p.cfg.NodeGroups {
		prefix := p.cfg.NamePrefix + ng.ID + "-"
		if strings.HasPrefix(srv.Name, prefix) {
			return &pb.NodeGroupForNodeResponse{
				NodeGroup: &pb.NodeGroup{Id: ng.ID, MinSize: int32(ng.MinSize), MaxSize: int32(ng.MaxSize)},
			}, nil
		}
	}
	return &pb.NodeGroupForNodeResponse{NodeGroup: &pb.NodeGroup{}}, nil
}

func (p *Provider) Refresh(ctx context.Context, _ *pb.RefreshRequest) (*pb.RefreshResponse, error) {
	servers, err := p.bl.ListServers(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "refreshing servers: %v", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.servers = make(map[int]*binarylane.Server)
	for i := range servers {
		s := &servers[i]
		if strings.HasPrefix(s.Name, p.cfg.NamePrefix) {
			p.servers[s.ID] = s
		}
	}
	slog.Info("refreshed server list", "managed_count", len(p.servers))
	return &pb.RefreshResponse{}, nil
}

func (p *Provider) Cleanup(_ context.Context, _ *pb.CleanupRequest) (*pb.CleanupResponse, error) {
	return &pb.CleanupResponse{}, nil
}

func (p *Provider) GPULabel(_ context.Context, _ *pb.GPULabelRequest) (*pb.GPULabelResponse, error) {
	return &pb.GPULabelResponse{}, nil
}

func (p *Provider) GetAvailableGPUTypes(_ context.Context, _ *pb.GetAvailableGPUTypesRequest) (*pb.GetAvailableGPUTypesResponse, error) {
	return &pb.GetAvailableGPUTypesResponse{}, nil
}

func (p *Provider) PricingNodePrice(_ context.Context, _ *pb.PricingNodePriceRequest) (*pb.PricingNodePriceResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "not implemented")
}

func (p *Provider) PricingPodPrice(_ context.Context, _ *pb.PricingPodPriceRequest) (*pb.PricingPodPriceResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "not implemented")
}

func (p *Provider) NodeGroupTargetSize(_ context.Context, req *pb.NodeGroupTargetSizeRequest) (*pb.NodeGroupTargetSizeResponse, error) {
	if ng := p.findGroup(req.Id); ng == nil {
		return nil, status.Errorf(codes.NotFound, "node group %s not found", req.Id)
	}
	return &pb.NodeGroupTargetSizeResponse{TargetSize: int32(len(p.serversForGroup(req.Id)))}, nil
}

func (p *Provider) NodeGroupIncreaseSize(ctx context.Context, req *pb.NodeGroupIncreaseSizeRequest) (*pb.NodeGroupIncreaseSizeResponse, error) {
	ng := p.findGroup(req.Id)
	if ng == nil {
		return nil, status.Errorf(codes.NotFound, "node group %s not found", req.Id)
	}
	current := p.serversForGroup(req.Id)
	if int(req.Delta)+len(current) > ng.MaxSize {
		return nil, status.Errorf(codes.InvalidArgument, "increase would exceed max size %d", ng.MaxSize)
	}
	for i := int32(0); i < req.Delta; i++ {
		name := fmt.Sprintf("%s%s-%s", p.cfg.NamePrefix, ng.ID, time.Now().Format("20060102150405"))
		if req.Delta > 1 {
			name = fmt.Sprintf("%s-%d", name, i)
		}
		vars := make(map[string]string)
		for k, v := range p.cfg.TemplateVars {
			vars[k] = v
		}
		vars["NodeName"] = name
		vars["NodeGroup"] = ng.ID
		vars["Region"] = ng.Region
		vars["Size"] = ng.Size
		var userData bytes.Buffer
		if err := p.cloudInit.Execute(&userData, vars); err != nil {
			return nil, status.Errorf(codes.Internal, "rendering cloud-init: %v", err)
		}
		slog.Info("creating server", "name", name, "size", ng.Size, "region", ng.Region)
		srv, err := p.bl.CreateServer(ctx, binarylane.CreateServerRequest{
			Name: name, Size: ng.Size, Image: ng.Image, Region: ng.Region,
			UserData: userData.String(), SSHKeys: p.cfg.SSHKeys,
		})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "creating server: %v", err)
		}
		p.mu.Lock()
		p.servers[srv.ID] = srv
		p.mu.Unlock()
		slog.Info("created server", "name", name, "id", srv.ID)
	}
	return &pb.NodeGroupIncreaseSizeResponse{}, nil
}

func (p *Provider) NodeGroupDeleteNodes(ctx context.Context, req *pb.NodeGroupDeleteNodesRequest) (*pb.NodeGroupDeleteNodesResponse, error) {
	if ng := p.findGroup(req.Id); ng == nil {
		return nil, status.Errorf(codes.NotFound, "node group %s not found", req.Id)
	}
	for _, node := range req.Nodes {
		serverID, ok := binarylane.ParseProviderID(node.ProviderID)
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "invalid provider ID: %s", node.ProviderID)
		}
		slog.Info("deleting server", "id", serverID)
		if err := p.bl.DeleteServer(ctx, serverID); err != nil {
			return nil, status.Errorf(codes.Internal, "deleting server: %v", err)
		}
		p.mu.Lock()
		delete(p.servers, serverID)
		p.mu.Unlock()
	}
	return &pb.NodeGroupDeleteNodesResponse{}, nil
}

func (p *Provider) NodeGroupDecreaseTargetSize(_ context.Context, req *pb.NodeGroupDecreaseTargetSizeRequest) (*pb.NodeGroupDecreaseTargetSizeResponse, error) {
	if ng := p.findGroup(req.Id); ng == nil {
		return nil, status.Errorf(codes.NotFound, "node group %s not found", req.Id)
	}
	return &pb.NodeGroupDecreaseTargetSizeResponse{}, nil
}

func (p *Provider) NodeGroupNodes(_ context.Context, req *pb.NodeGroupNodesRequest) (*pb.NodeGroupNodesResponse, error) {
	if ng := p.findGroup(req.Id); ng == nil {
		return nil, status.Errorf(codes.NotFound, "node group %s not found", req.Id)
	}
	servers := p.serversForGroup(req.Id)
	var instances []*pb.Instance
	for _, s := range servers {
		state := pb.InstanceStatus_unspecified
		switch s.Status {
		case "active":
			state = pb.InstanceStatus_instanceRunning
		case "new":
			state = pb.InstanceStatus_instanceCreating
		}
		instances = append(instances, &pb.Instance{
			Id:     binarylane.ServerProviderID(s.ID),
			Status: &pb.InstanceStatus{InstanceState: state},
		})
	}
	return &pb.NodeGroupNodesResponse{Instances: instances}, nil
}

func (p *Provider) NodeGroupTemplateNodeInfo(_ context.Context, req *pb.NodeGroupTemplateNodeInfoRequest) (*pb.NodeGroupTemplateNodeInfoResponse, error) {
	ng := p.findGroup(req.Id)
	if ng == nil {
		return nil, status.Errorf(codes.NotFound, "node group %s not found", req.Id)
	}
	labels := map[string]string{
		"node.kubernetes.io/instance-type":  ng.Size,
		"topology.kubernetes.io/region":     ng.Region,
		"kubernetes.io/arch":                "amd64",
		"kubernetes.io/os":                  "linux",
		"node.kubernetes.io/cloud-provider": binarylane.ProviderName,
	}
	for k, v := range ng.Labels {
		labels[k] = v
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("template-%s", ng.ID),
			Labels: labels,
		},
		Spec: corev1.NodeSpec{
			ProviderID: binarylane.ProviderName + ":///template",
			Taints: []corev1.Taint{{
				Key: "node.cloudprovider.kubernetes.io/uninitialized", Value: "true",
				Effect: corev1.TaintEffectNoSchedule,
			}},
		},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:              resource.MustParse(fmt.Sprintf("%d", ng.VCPUs)),
				corev1.ResourceMemory:           resource.MustParse(fmt.Sprintf("%dMi", ng.MemoryMB)),
				corev1.ResourceEphemeralStorage: resource.MustParse(fmt.Sprintf("%dGi", ng.DiskGB)),
				corev1.ResourcePods:             resource.MustParse("110"),
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:              resource.MustParse(fmt.Sprintf("%dm", ng.VCPUs*1000-100)),
				corev1.ResourceMemory:           resource.MustParse(fmt.Sprintf("%dMi", ng.MemoryMB-256)),
				corev1.ResourceEphemeralStorage: resource.MustParse(fmt.Sprintf("%dGi", ng.DiskGB-1)),
				corev1.ResourcePods:             resource.MustParse("110"),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	nodeBytes, err := node.Marshal()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshaling template node: %v", err)
	}
	return &pb.NodeGroupTemplateNodeInfoResponse{NodeInfo: nodeBytes}, nil
}

func (p *Provider) NodeGroupGetOptions(_ context.Context, _ *pb.NodeGroupAutoscalingOptionsRequest) (*pb.NodeGroupAutoscalingOptionsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "not implemented")
}
