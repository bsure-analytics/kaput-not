package leaderelection

import (
	"context"
	"fmt"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// Config contains configuration for leader election
type Config struct {
	// KubeClient is the Kubernetes client
	KubeClient kubernetes.Interface

	// LockName is the name of the lease resource
	LockName string

	// LockNamespace is the namespace for the lease resource
	LockNamespace string

	// Identity is the unique identity of this replica (defaults to hostname)
	Identity string

	// LeaseDuration is how long the leader lease is valid
	// Default: 15 seconds
	LeaseDuration time.Duration

	// RenewDeadline is the deadline for the leader to renew the lease
	// Default: 10 seconds
	RenewDeadline time.Duration

	// RetryPeriod is how often non-leaders will try to acquire leadership
	// Default: 2 seconds
	RetryPeriod time.Duration

	// OnStartedLeading is called when this replica becomes the leader
	OnStartedLeading func(ctx context.Context)

	// OnStoppedLeading is called when this replica stops being the leader
	OnStoppedLeading func()

	// OnNewLeader is called when a new leader is elected
	OnNewLeader func(identity string)
}

// Validate validates the configuration
func (c *Config) Validate() error {
	if c.KubeClient == nil {
		return fmt.Errorf("KubeClient is required")
	}
	if c.LockName == "" {
		return fmt.Errorf("LockName is required")
	}
	if c.LockNamespace == "" {
		return fmt.Errorf("LockNamespace is required")
	}
	if c.OnStartedLeading == nil {
		return fmt.Errorf("OnStartedLeading is required")
	}
	return nil
}

// ApplyDefaults applies default values to the configuration
func (c *Config) ApplyDefaults() {
	if c.Identity == "" {
		hostname, err := os.Hostname()
		if err != nil {
			c.Identity = "unknown"
		} else {
			c.Identity = hostname
		}
	}

	if c.LeaseDuration == 0 {
		c.LeaseDuration = 15 * time.Second
	}

	if c.RenewDeadline == 0 {
		c.RenewDeadline = 10 * time.Second
	}

	if c.RetryPeriod == 0 {
		c.RetryPeriod = 2 * time.Second
	}

	if c.OnStoppedLeading == nil {
		c.OnStoppedLeading = func() {}
	}

	if c.OnNewLeader == nil {
		c.OnNewLeader = func(identity string) {}
	}
}

// Run starts the leader election and blocks until the context is canceled
// Only the leader will execute OnStartedLeading callback
func Run(ctx context.Context, config *Config) error {
	// Validate and apply defaults
	if err := config.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	config.ApplyDefaults()

	// Create resource lock using Lease (recommended for Kubernetes 1.14+)
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      config.LockName,
			Namespace: config.LockNamespace,
		},
		Client: config.KubeClient.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: config.Identity,
		},
	}

	// Create leader elector
	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:            lock,
		ReleaseOnCancel: true,
		LeaseDuration:   config.LeaseDuration,
		RenewDeadline:   config.RenewDeadline,
		RetryPeriod:     config.RetryPeriod,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: config.OnStartedLeading,
			OnStoppedLeading: config.OnStoppedLeading,
			OnNewLeader:      config.OnNewLeader,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create leader elector: %w", err)
	}

	// Run the leader election (blocks until context is canceled)
	elector.Run(ctx)

	return nil
}
