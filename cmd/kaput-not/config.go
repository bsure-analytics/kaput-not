package main

import (
	"fmt"
	"os"
	"strconv"
)

const (
	// serviceAccountNamespaceFile is the path to the namespace file mounted in pods
	serviceAccountNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

// Config holds all configuration loaded from environment variables
type Config struct {
	// Netmaker configuration
	NetmakerAPIURL   string
	NetmakerUsername string
	NetmakerPassword string
	// Networks are auto-discovered by looking up Netmaker host nodes

	// Kubernetes configuration
	Kubeconfig string // Optional - empty means in-cluster

	// Leader election configuration
	LeaderElectionEnabled   bool
	LeaderElectionNamespace string
	LeaderElectionID        string
}

// LoadConfig loads configuration from environment variables
// Following twelve-factor app principles, all configuration comes from env vars
// Auto-detects in-cluster vs local environment for smart defaults
func LoadConfig() (*Config, error) {
	// Detect if running in-cluster
	inCluster := isInCluster()

	cfg := &Config{
		// Netmaker configuration (required)
		NetmakerAPIURL:   os.Getenv("NETMAKER_API_URL"),
		NetmakerUsername: os.Getenv("NETMAKER_USERNAME"),
		NetmakerPassword: os.Getenv("NETMAKER_PASSWORD"),
		// Networks are auto-discovered by querying Netmaker

		// Kubernetes configuration (optional)
		Kubeconfig: os.Getenv("KUBECONFIG"),

		// Leader election configuration (auto-detected with overrides)
		LeaderElectionEnabled:   detectLeaderElection(inCluster),
		LeaderElectionNamespace: detectNamespace(inCluster),
		LeaderElectionID:        getEnvWithDefault("LEADER_ELECTION_ID", "kaput-not"),
	}

	// Validate required fields
	if cfg.NetmakerAPIURL == "" {
		return nil, fmt.Errorf("NETMAKER_API_URL is required")
	}
	if cfg.NetmakerUsername == "" {
		return nil, fmt.Errorf("NETMAKER_USERNAME is required")
	}
	if cfg.NetmakerPassword == "" {
		return nil, fmt.Errorf("NETMAKER_PASSWORD is required")
	}

	return cfg, nil
}

// isInCluster checks if the process is running inside a Kubernetes cluster
// by checking for the existence of the service account namespace file
func isInCluster() bool {
	_, err := os.Stat(serviceAccountNamespaceFile)
	return err == nil
}

// detectNamespace auto-detects the namespace for leader election
// In-cluster: reads from service account namespace file
// Local: uses LEADER_ELECTION_NAMESPACE env var or "kube-system" as fallback
func detectNamespace(inCluster bool) string {
	// Check for explicit override first
	if envNamespace := os.Getenv("LEADER_ELECTION_NAMESPACE"); envNamespace != "" {
		return envNamespace
	}

	// In-cluster: read from service account
	if inCluster {
		if data, err := os.ReadFile(serviceAccountNamespaceFile); err == nil {
			return string(data)
		}
	}

	// Fallback to kube-system (common default for cluster-wide controllers)
	return "kube-system"
}

// detectLeaderElection auto-detects if leader election should be enabled
// In-cluster: enabled by default (HA)
// Local: disabled by default (single dev instance)
// Can be overridden via LEADER_ELECTION_ENABLED env var
func detectLeaderElection(inCluster bool) bool {
	// Check for explicit override first
	if envValue := os.Getenv("LEADER_ELECTION_ENABLED"); envValue != "" {
		return parseBool(envValue, inCluster)
	}

	// Auto-detect based on environment
	return inCluster
}

// getEnvWithDefault returns the environment variable value or a default if not set
func getEnvWithDefault(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

// parseBool parses a boolean environment variable
// Accepts: "true", "false", "1", "0" (case-insensitive)
// Returns defaultValue if the value is invalid
func parseBool(value string, defaultValue bool) bool {
	switch value {
	case "true", "True", "TRUE", "1":
		return true
	case "false", "False", "FALSE", "0":
		return false
	default:
		// Invalid value - return default
		return defaultValue
	}
}

// getEnvInt returns the environment variable as an integer or a default if not set
func getEnvInt(key string, defaultValue int) int {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}

	intValue, err := strconv.Atoi(value)
	if err != nil {
		return defaultValue
	}

	return intValue
}
