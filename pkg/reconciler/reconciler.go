package reconciler

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/bsure-analytics/kaput-not/pkg/netmaker"
)

const (
	// EgressMarker is the prefix for managed egress rule descriptions
	EgressMarker = "Managed by kaput-not (DO NOT EDIT)"
	// EgressMetric is the metric value used for egress gateway nodes
	EgressMetric = 500
)

// Reconciler handles Node reconciliation logic
// Networks are auto-discovered by looking up which networks the Netmaker host participates in
type Reconciler struct {
	netmakerClient *netmaker.CachedClient
	clusterName    string // Optional - for multi-cluster deployments sharing a Netmaker network
}

// New creates a new reconciler with a single cached client
// Networks are discovered automatically per K8s node
// clusterName is optional - if set, egress rules will be scoped to this cluster
func New(client *netmaker.CachedClient, clusterName string) *Reconciler {
	return &Reconciler{
		netmakerClient: client,
		clusterName:    clusterName,
	}
}

// ReconcileNode syncs a Node's pod CIDRs to Netmaker egress rules
// Networks are auto-discovered from the Netmaker nodes themselves
// Returns error with full context, never panics
//
// Algorithm:
//  1. Extract pod CIDRs from node
//  2. Get all Netmaker node IDs for this host (from host.Nodes field)
//  3. Get all nodes across all networks
//  4. For each node belonging to this host, reconcile egress rules in its network
func (r *Reconciler) ReconcileNode(ctx context.Context, node *corev1.Node) error {
	podCIDRs := node.Spec.PodCIDRs

	if len(podCIDRs) == 0 {
		// Not an error - node might not have CIDRs assigned yet
		return nil
	}

	// Get all Netmaker node IDs for this host (from host.Nodes field)
	nodeIDs, err := r.netmakerClient.GetNodeIDsByHostname(ctx, node.Name)
	if err != nil {
		// If host doesn't exist, skip silently (not an error)
		if strings.Contains(err.Error(), "not found") {
			return nil
		}
		return fmt.Errorf("failed to get node IDs for node %s: %w", node.Name, err)
	}

	if len(nodeIDs) == 0 {
		// No nodes for this host - skip silently
		return nil
	}

	// Get all nodes - each node contains its network
	allNodes, err := r.netmakerClient.ListNodes(ctx)
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	// Reconcile each node that belongs to this host
	// Each node tells us both the nodeID and which network it's in
	var reconcileErrors []error
	for _, n := range allNodes {
		// Check if this node belongs to our host
		belongsToHost := false
		for _, id := range nodeIDs {
			if n.ID == id {
				belongsToHost = true
				break
			}
		}

		if !belongsToHost {
			continue
		}

		// Reconcile egress rules for this node in its network
		if err := r.reconcileNodeInNetwork(ctx, node, podCIDRs, n.ID, n.Network); err != nil {
			// Collect errors but continue with other nodes
			reconcileErrors = append(reconcileErrors, fmt.Errorf("network %s: %w", n.Network, err))
		}
	}

	if len(reconcileErrors) > 0 {
		return fmt.Errorf("failed to reconcile node %s in some networks: %v", node.Name, reconcileErrors)
	}

	return nil
}

// reconcileNodeInNetwork reconciles a single node in a single network
// nodeID is passed as parameter - no lookup needed
func (r *Reconciler) reconcileNodeInNetwork(ctx context.Context, node *corev1.Node, podCIDRs []string, nodeID string, network string) error {

	// List all existing egress rules for this network
	existingEgresses, err := r.netmakerClient.ListEgress(ctx, network)
	if err != nil {
		return fmt.Errorf("failed to list egress rules in network %s: %w", network, err)
	}

	// Reconcile each pod CIDR
	for index, podCIDR := range podCIDRs {
		if err := r.reconcilePodCIDR(ctx, node.Name, nodeID, podCIDR, index, len(podCIDRs), existingEgresses, network); err != nil {
			return fmt.Errorf("failed to reconcile pod CIDR %s (index=%d) in network %s: %w", podCIDR, index, network, err)
		}
	}

	return nil
}

// reconcilePodCIDR reconciles a single pod CIDR in a single network
func (r *Reconciler) reconcilePodCIDR(
	ctx context.Context,
	nodeName string,
	nodeID string,
	podCIDR string,
	index int,
	totalCIDRs int,
	existingEgresses []netmaker.Egress,
	network string,
) error {
	// Build index-based description: "Managed by kaput-not (DO NOT EDIT): index=<i>"
	// or with cluster: "Managed by kaput-not (DO NOT EDIT): cluster=us-east index=<i>"
	description := r.buildEgressDescription(index)

	// Build human-friendly name: "node-name pods (1/2)"
	name := buildEgressName(nodeName, index, totalCIDRs)

	// Search for existing egress rule with matching index AND node ID in nodes map
	// Supports both old format (index=0) and new format (cluster=us-east index=0)
	var existingEgress *netmaker.Egress
	for i := range existingEgresses {
		// Parse description to extract metadata
		metadata := parseEgressDescription(existingEgresses[i].Description)
		if metadata == nil {
			continue // Not a kaput-not managed egress
		}

		// Check if this egress belongs to our cluster
		if !r.belongsToOurCluster(metadata) {
			continue // Managed by another cluster or incompatible mode
		}

		// Check if index matches
		if metadata.index != index {
			continue
		}

		// Check if this egress belongs to our node (node ID in nodes map)
		if _, hasNode := existingEgresses[i].Nodes[nodeID]; hasNode {
			existingEgress = &existingEgresses[i]
			break
		}
	}

	if existingEgress != nil {
		// Egress exists - check if CIDR matches
		if existingEgress.Range == podCIDR {
			// Already correct - skip
			return nil
		}

		// CIDR changed - update existing egress
		req := netmaker.EgressReq{
			ID:          existingEgress.ID,
			Name:        name,
			Network:     existingEgress.Network,
			Description: description,
			Range:       podCIDR,
			NAT:         false,
			Nodes:       map[string]int{nodeID: EgressMetric},
			Status:      true,
		}

		_, err := r.netmakerClient.UpdateEgress(ctx, req)
		if err != nil {
			return fmt.Errorf("failed to update egress %s (old CIDR=%s, new CIDR=%s): %w",
				existingEgress.ID, existingEgress.Range, podCIDR, err)
		}

		return nil
	}

	// Egress doesn't exist - create new one
	req := netmaker.EgressReq{
		Name:        name,
		Network:     network,
		Description: description,
		Range:       podCIDR,
		NAT:         false,
		Nodes:       map[string]int{nodeID: EgressMetric},
		Status:      true,
	}

	_, err := r.netmakerClient.CreateEgress(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create egress for CIDR %s: %w", podCIDR, err)
	}

	return nil
}

// DeleteNode removes egress rules for a deleted node from all networks it participated in
// Networks are auto-discovered from the Netmaker nodes themselves
// Searches for all egress rules that have this node ID in their nodes map
func (r *Reconciler) DeleteNode(ctx context.Context, nodeName string) error {
	// Get all Netmaker node IDs for this host (from host.Nodes field)
	nodeIDs, err := r.netmakerClient.GetNodeIDsByHostname(ctx, nodeName)
	if err != nil {
		// If host doesn't exist, skip silently (nothing to delete)
		if strings.Contains(err.Error(), "not found") {
			return nil
		}
		return fmt.Errorf("failed to get node IDs for node %s: %w", nodeName, err)
	}

	if len(nodeIDs) == 0 {
		// No nodes for this host - nothing to delete
		return nil
	}

	// Get all nodes - each node contains its network
	allNodes, err := r.netmakerClient.ListNodes(ctx)
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	// Delete egress rules for each node that belongs to this host
	var deletionErrors []error
	for _, n := range allNodes {
		// Check if this node belongs to our host
		belongsToHost := false
		for _, id := range nodeIDs {
			if n.ID == id {
				belongsToHost = true
				break
			}
		}

		if !belongsToHost {
			continue
		}

		// Delete egress rules for this node in its network
		if err := r.deleteNodeFromNetwork(ctx, n.ID, n.Network); err != nil {
			deletionErrors = append(deletionErrors, fmt.Errorf("network %s: %w", n.Network, err))
		}
	}

	if len(deletionErrors) > 0 {
		return fmt.Errorf("failed to delete node %s from some networks: %v", nodeName, deletionErrors)
	}

	return nil
}

// deleteNodeFromNetwork removes egress rules for a node in a single network
// nodeID is passed as parameter - no lookup needed
// Only deletes egress rules that belong to this cluster
func (r *Reconciler) deleteNodeFromNetwork(ctx context.Context, nodeID string, network string) error {

	// List all egress rules for this network
	egresses, err := r.netmakerClient.ListEgress(ctx, network)
	if err != nil {
		return fmt.Errorf("failed to list egress rules in network %s: %w", network, err)
	}

	// Find and delete all egress rules managed by kaput-not that contain this node ID
	var deletionErrors []error
	for _, egress := range egresses {
		// Parse description to extract metadata
		metadata := parseEgressDescription(egress.Description)
		if metadata == nil {
			continue // Not a kaput-not managed egress
		}

		// Check if this egress belongs to our cluster
		if !r.belongsToOurCluster(metadata) {
			continue // Managed by another cluster or incompatible mode
		}

		// Check if this node ID is in the egress nodes map
		if _, hasNode := egress.Nodes[nodeID]; hasNode {
			if err := r.netmakerClient.DeleteEgress(ctx, egress.ID); err != nil {
				deletionErrors = append(deletionErrors, fmt.Errorf("failed to delete egress %s in network %s: %w", egress.ID, network, err))
			}
		}
	}

	if len(deletionErrors) > 0 {
		return fmt.Errorf("failed to delete some egress rules in network %s: %v", network, deletionErrors)
	}

	return nil
}

// CleanupOrphanedEgresses removes egress rules for Netmaker nodes that don't have corresponding K8s nodes
// This handles drift detection - egress rules created manually or left behind when the controller was down
// validNodeIDs is the set of all Netmaker node IDs that should have egress rules
func (r *Reconciler) CleanupOrphanedEgresses(ctx context.Context, validNodeIDs map[string]bool) error {
	// Get all nodes across all networks
	allNodes, err := r.netmakerClient.ListNodes(ctx)
	if err != nil {
		return fmt.Errorf("failed to list all nodes: %w", err)
	}

	// Group nodes by network for efficient cleanup
	networkNodes := make(map[string][]string) // network -> []nodeID
	for _, node := range allNodes {
		networkNodes[node.Network] = append(networkNodes[node.Network], node.ID)
	}

	// Clean up each network
	var cleanupErrors []error
	for network, nodeIDs := range networkNodes {
		// Find orphaned node IDs (nodes in Netmaker but not in K8s)
		var orphanedNodeIDs []string
		for _, nodeID := range nodeIDs {
			if !validNodeIDs[nodeID] {
				orphanedNodeIDs = append(orphanedNodeIDs, nodeID)
			}
		}

		// Delete egress rules for orphaned nodes
		for _, nodeID := range orphanedNodeIDs {
			if err := r.deleteNodeFromNetwork(ctx, nodeID, network); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("network %s, node %s: %w", network, nodeID, err))
			}
		}
	}

	if len(cleanupErrors) > 0 {
		return fmt.Errorf("failed to cleanup some orphaned egress rules: %v", cleanupErrors)
	}

	return nil
}

// egressMetadata holds parsed metadata from an egress description
type egressMetadata struct {
	cluster string // empty if not present (backwards compatible)
	index   int
}

// parseEgressDescription parses the egress description to extract metadata
// Supports both formats:
//   - New: "Managed by kaput-not (DO NOT EDIT): cluster=us-east index=0"
//   - Old: "Managed by kaput-not (DO NOT EDIT): index=0"
//
// Returns nil if description doesn't match expected format
func parseEgressDescription(description string) *egressMetadata {
	// Check if it starts with our marker
	if !strings.HasPrefix(description, EgressMarker+": ") {
		return nil
	}

	// Extract metadata part after the marker
	metadataPart := strings.TrimPrefix(description, EgressMarker+": ")

	// Parse space-separated key=value pairs
	metadata := &egressMetadata{}
	fields := strings.Fields(metadataPart)

	for _, field := range fields {
		kv := strings.SplitN(field, "=", 2)
		if len(kv) != 2 {
			continue
		}

		switch kv[0] {
		case "cluster":
			metadata.cluster = kv[1]
		case "index":
			// Ignore error - if parsing fails, index stays at zero value
			_, _ = fmt.Sscanf(kv[1], "%d", &metadata.index)
		}
	}

	return metadata
}

// belongsToOurCluster checks if an egress rule belongs to our cluster
// Logic:
//   - If our cluster name is empty: all kaput-not egress rules belong to us (single-cluster mode)
//   - If our cluster name is set: only egress rules with matching cluster name belong to us
//   - Egress rules without cluster name are skipped in multi-cluster mode (migration safety)
func (r *Reconciler) belongsToOurCluster(metadata *egressMetadata) bool {
	if metadata == nil {
		return false // Not a kaput-not managed egress
	}

	// Single-cluster mode (no cluster name configured)
	if r.clusterName == "" {
		// If egress has a cluster name, it's from another cluster
		// Only manage egress rules without cluster name (backwards compatibility)
		return metadata.cluster == ""
	}

	// Multi-cluster mode (cluster name configured)
	// Only manage egress rules with our cluster name
	return metadata.cluster == r.clusterName
}

// buildEgressDescription builds the index-based description
// Format with cluster: "Managed by kaput-not (DO NOT EDIT): cluster=us-east index=0"
// Format without: "Managed by kaput-not (DO NOT EDIT): index=0"
func (r *Reconciler) buildEgressDescription(index int) string {
	if r.clusterName != "" {
		return fmt.Sprintf("%s: cluster=%s index=%d", EgressMarker, r.clusterName, index)
	}
	return fmt.Sprintf("%s: index=%d", EgressMarker, index)
}

// buildEgressName builds the human-friendly egress name
// Format: "node-name pods (1/2)"
func buildEgressName(nodeName string, index int, totalCIDRs int) string {
	return fmt.Sprintf("%s pods (%d/%d)", nodeName, index+1, totalCIDRs)
}
