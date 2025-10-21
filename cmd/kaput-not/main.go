package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/bsure-analytics/kaput-not/pkg/controller"
	"github.com/bsure-analytics/kaput-not/pkg/leaderelection"
	"github.com/bsure-analytics/kaput-not/pkg/netmaker"
	"github.com/bsure-analytics/kaput-not/pkg/reconciler"
)

func main() {
	// Setup logging
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.Println("Starting kaput-not Kubernetes controller...")

	// Load configuration from environment
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	log.Printf("Configuration loaded: api=%s, leader-election=%v (networks auto-discovered)",
		cfg.NetmakerAPIURL, cfg.LeaderElectionEnabled)

	// Create Kubernetes client
	kubeClient, err := createKubeClient(cfg.Kubeconfig)
	if err != nil {
		log.Fatalf("Failed to create Kubernetes client: %v", err)
	}
	log.Println("Kubernetes client created successfully")

	// Create single Netmaker client for all networks
	ctx := context.Background()

	// Create HTTP client (works with all networks)
	httpClient, err := netmaker.NewHTTPClient(
		cfg.NetmakerAPIURL,
		cfg.NetmakerUsername,
		cfg.NetmakerPassword,
	)
	if err != nil {
		log.Fatalf("Failed to create Netmaker HTTP client: %v", err)
	}

	// Wrap with caching layer (30 second TTL, shared across all networks)
	cachedClient := netmaker.NewCachedClient(httpClient, 0)

	// Authenticate immediately to validate credentials
	if err := cachedClient.Authenticate(ctx); err != nil {
		log.Fatalf("Failed to authenticate with Netmaker: %v", err)
	}
	log.Println("Successfully authenticated with Netmaker")

	// Create reconciler with single client (networks auto-discovered)
	rec := reconciler.New(cachedClient)
	log.Println("Reconciler created successfully")

	// Create controller
	ctrl, err := controller.New(&controller.Options{
		KubeClient:     kubeClient,
		NetmakerClient: cachedClient,
		Reconciler:     rec,
	})
	if err != nil {
		log.Fatalf("Failed to create controller: %v", err)
	}
	log.Println("Controller created successfully")

	// Setup signal handling for graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Run with or without leader election
	if cfg.LeaderElectionEnabled {
		log.Printf("Leader election enabled: namespace=%s, id=%s",
			cfg.LeaderElectionNamespace, cfg.LeaderElectionID)
		runWithLeaderElection(ctx, kubeClient, ctrl, cfg)
	} else {
		log.Println("Leader election disabled - running as single replica")
		runWithoutLeaderElection(ctx, ctrl)
	}

	log.Println("Shutting down gracefully...")
}

// createKubeClient creates a Kubernetes client
// If kubeconfig is empty, uses in-cluster configuration
func createKubeClient(kubeconfig string) (kubernetes.Interface, error) {
	var config *rest.Config
	var err error

	if kubeconfig == "" {
		// In-cluster: read service account token and CA cert
		log.Println("Using in-cluster Kubernetes configuration")
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, err
		}
	} else {
		// Local development: load from kubeconfig file
		log.Printf("Using kubeconfig from: %s", kubeconfig)
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, err
		}
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return client, nil
}

// runWithLeaderElection runs the controller with leader election
// Only the elected leader will run the controller
func runWithLeaderElection(ctx context.Context, kubeClient kubernetes.Interface, ctrl *controller.Controller, cfg *Config) {
	// Create leader election config
	leConfig := &leaderelection.Config{
		KubeClient:    kubeClient,
		LockName:      cfg.LeaderElectionID,
		LockNamespace: cfg.LeaderElectionNamespace,
		OnStartedLeading: func(ctx context.Context) {
			log.Println("*** Became leader - starting controller ***")
			if err := ctrl.Run(ctx); err != nil {
				log.Fatalf("Controller failed: %v", err)
			}
		},
		OnStoppedLeading: func() {
			log.Println("*** Lost leadership - exiting ***")
			// Exit the process - Kubernetes will restart it
			os.Exit(0)
		},
		OnNewLeader: func(identity string) {
			hostname, _ := os.Hostname()
			if identity == hostname {
				log.Printf("*** I am the new leader: %s ***", identity)
			} else {
				log.Printf("New leader elected: %s (I am: %s)", identity, hostname)
			}
		},
	}

	// Run leader election (blocks until context is cancelled)
	if err := leaderelection.Run(ctx, leConfig); err != nil {
		log.Fatalf("Leader election failed: %v", err)
	}
}

// runWithoutLeaderElection runs the controller directly without leader election
func runWithoutLeaderElection(ctx context.Context, ctrl *controller.Controller) {
	if err := ctrl.Run(ctx); err != nil {
		log.Fatalf("Controller failed: %v", err)
	}
}
