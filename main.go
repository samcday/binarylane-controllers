package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"strings"

	"github.com/samcday/binarylane-controller/autoscaler"
	"github.com/samcday/binarylane-controller/binarylane"
	"github.com/samcday/binarylane-controller/nodecontroller"
	pb "github.com/samcday/binarylane-controller/proto"
	"google.golang.org/grpc"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	blToken := os.Getenv("BL_API_TOKEN")
	if blToken == "" {
		slog.Error("BL_API_TOKEN is required")
		os.Exit(1)
	}

	bl := binarylane.NewClient(blToken)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start node controller if k8s access is available
	k8sConfig, err := buildKubeConfig()
	if err != nil {
		slog.Error("building kubeconfig", "error", err)
		os.Exit(1)
	}
	k8sClient, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		slog.Error("creating kubernetes client", "error", err)
		os.Exit(1)
	}
	nc := nodecontroller.New(bl, k8sClient)
	go nc.Run(ctx)

	// Start autoscaler gRPC provider if config is available
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "/etc/binarylane-controller/config.json"
	}
	cloudInitPath := os.Getenv("CLOUD_INIT_PATH")
	if cloudInitPath == "" {
		cloudInitPath = "/etc/binarylane-controller/cloud-init.sh"
	}
	listenAddr := os.Getenv("GRPC_LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8086"
	}

	cfgData, err := os.ReadFile(configPath)
	if err != nil {
		slog.Warn("autoscaler config not found, gRPC provider disabled", "path", configPath, "error", err)
		// Block on node controller only
		<-ctx.Done()
		return
	}

	var cfg autoscaler.Config
	if err := json.Unmarshal(cfgData, &cfg); err != nil {
		slog.Error("parsing autoscaler config", "error", err)
		os.Exit(1)
	}

	cloudInitData, err := os.ReadFile(cloudInitPath)
	if err != nil {
		slog.Error("reading cloud-init template", "error", err)
		os.Exit(1)
	}
	cfg.CloudInit = string(cloudInitData)

	cfg.TemplateVars = make(map[string]string)
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "TMPL_") {
			parts := strings.SplitN(env, "=", 2)
			cfg.TemplateVars[strings.TrimPrefix(parts[0], "TMPL_")] = parts[1]
		}
	}

	provider, err := autoscaler.NewProvider(bl, cfg)
	if err != nil {
		slog.Error("creating autoscaler provider", "error", err)
		os.Exit(1)
	}

	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		slog.Error("listening", "error", err, "addr", listenAddr)
		os.Exit(1)
	}

	srv := grpc.NewServer()
	pb.RegisterCloudProviderServer(srv, provider)

	slog.Info("binarylane-controller starting", "grpc", listenAddr, "nodeGroups", len(cfg.NodeGroups))
	if err := srv.Serve(lis); err != nil {
		slog.Error("gRPC server error", "error", err)
		os.Exit(1)
	}
}

func buildKubeConfig() (*rest.Config, error) {
	if kubeconfig := os.Getenv("KUBECONFIG"); kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	return rest.InClusterConfig()
}
