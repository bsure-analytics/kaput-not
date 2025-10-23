package controller

import (
	"fmt"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/bsure-analytics/kaput-not/pkg/netmaker"
	"github.com/bsure-analytics/kaput-not/pkg/reconciler"
)

// Options contains configuration for the controller
type Options struct {
	// KubeClient is the Kubernetes client
	KubeClient kubernetes.Interface

	// NetmakerClient is the Netmaker API client
	NetmakerClient netmaker.Client

	// Reconciler is the reconciliation logic
	Reconciler *reconciler.Reconciler

	// ClusterName is the name of this Kubernetes cluster (optional, for multi-cluster deployments)
	ClusterName string

	// ResyncPeriod is how often to resync all nodes
	// Default: 10 minutes
	ResyncPeriod time.Duration

	// WorkerCount is the number of concurrent reconciliation workers
	// Default: 1
	WorkerCount int
}

// Validate validates the options
func (o *Options) Validate() error {
	if o.KubeClient == nil {
		return fmt.Errorf("KubeClient is required")
	}
	if o.NetmakerClient == nil {
		return fmt.Errorf("NetmakerClient is required")
	}
	if o.Reconciler == nil {
		return fmt.Errorf("Reconciler is required")
	}
	return nil
}

// ApplyDefaults applies default values to options
func (o *Options) ApplyDefaults() {
	if o.ResyncPeriod == 0 {
		o.ResyncPeriod = 10 * time.Minute
	}
	if o.WorkerCount == 0 {
		o.WorkerCount = 1
	}
}
