package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

// Controller watches Kubernetes Node resources and synchronizes pod CIDRs to Netmaker
type Controller struct {
	options *Options

	nodeInformer cache.SharedIndexInformer
	workqueue    workqueue.TypedRateLimitingInterface[string]
}

// New creates a new controller
func New(opts *Options) (*Controller, error) {
	// Validate and apply defaults
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("invalid options: %w", err)
	}
	opts.ApplyDefaults()

	// Create node informer
	nodeInformerFactory := coreinformers.NewNodeInformer(
		opts.KubeClient,
		opts.ResyncPeriod,
		cache.Indexers{},
	)

	// Create workqueue with rate limiting
	workqueue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())

	c := &Controller{
		options:      opts,
		nodeInformer: nodeInformerFactory,
		workqueue:    workqueue,
	}

	// Register event handlers
	if _, err := c.nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.handleNodeAdd,
		UpdateFunc: c.handleNodeUpdate,
		DeleteFunc: c.handleNodeDelete,
	}); err != nil {
		return nil, fmt.Errorf("failed to add event handler: %w", err)
	}

	return c, nil
}

// Run starts the controller and blocks until the context is canceled
func (c *Controller) Run(ctx context.Context) error {
	defer runtime.HandleCrash()
	defer c.workqueue.ShutDown()

	// Start the informer
	go c.nodeInformer.Run(ctx.Done())

	// Wait for cache to sync
	if !cache.WaitForCacheSync(ctx.Done(), c.nodeInformer.HasSynced) {
		return fmt.Errorf("failed to wait for cache sync")
	}

	// Perform initial cleanup of orphaned egress rules
	if err := c.cleanupOrphanedEgresses(ctx); err != nil {
		runtime.HandleError(fmt.Errorf("initial cleanup failed: %w", err))
	}

	// Start workers
	for i := 0; i < c.options.WorkerCount; i++ {
		go wait.UntilWithContext(ctx, c.runWorker, time.Second)
	}

	// Start periodic cleanup goroutine (runs every ResyncPeriod)
	go wait.UntilWithContext(ctx, c.periodicCleanup, c.options.ResyncPeriod)

	<-ctx.Done()
	return nil
}

// runWorker processes items from the workqueue
func (c *Controller) runWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

// processNextWorkItem processes a single item from the workqueue
func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	key, shutdown := c.workqueue.Get()
	if shutdown {
		return false
	}

	defer c.workqueue.Done(key)

	if err := c.syncHandler(ctx, key); err != nil {
		c.workqueue.AddRateLimited(key)
		runtime.HandleError(fmt.Errorf("error syncing '%s': %w, requeuing", key, err))
		return true
	}

	c.workqueue.Forget(key)
	return true
}

// syncHandler processes a single node
func (c *Controller) syncHandler(ctx context.Context, key string) error {
	// Parse the key
	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return fmt.Errorf("invalid resource key: %s", key)
	}

	// Handle deletion
	if name == "" {
		// This is a delete event (key format: "DELETE:node-name")
		// Extract the node name from the key
		// Actually, we'll use a different approach - store delete events separately
		return nil
	}

	// Get node from cache
	obj, exists, err := c.nodeInformer.GetIndexer().GetByKey(key)
	if err != nil {
		return fmt.Errorf("failed to get node from cache: %w", err)
	}

	if !exists {
		// Node was deleted - handled separately in handleNodeDelete
		return nil
	}

	node, ok := obj.(*corev1.Node)
	if !ok {
		return fmt.Errorf("expected Node but got %T", obj)
	}

	// Reconcile the node
	if err := c.options.Reconciler.ReconcileNode(ctx, node); err != nil {
		return fmt.Errorf("failed to reconcile node %s: %w", node.Name, err)
	}

	return nil
}

// handleNodeAdd handles node creation events
func (c *Controller) handleNodeAdd(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	c.workqueue.Add(key)
}

// handleNodeUpdate handles node update events
func (c *Controller) handleNodeUpdate(oldObj, newObj interface{}) {
	oldNode, ok := oldObj.(*corev1.Node)
	if !ok {
		runtime.HandleError(fmt.Errorf("expected Node but got %T", oldObj))
		return
	}

	newNode, ok := newObj.(*corev1.Node)
	if !ok {
		runtime.HandleError(fmt.Errorf("expected Node but got %T", newObj))
		return
	}

	// Only reconcile if pod CIDRs changed
	if !podCIDRsChanged(oldNode, newNode) {
		return
	}

	key, err := cache.MetaNamespaceKeyFunc(newObj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	c.workqueue.Add(key)
}

// handleNodeDelete handles node deletion events
func (c *Controller) handleNodeDelete(obj interface{}) {
	node, ok := obj.(*corev1.Node)
	if !ok {
		// Handle tombstone (object was deleted but event came late)
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			runtime.HandleError(fmt.Errorf("expected Node or tombstone but got %T", obj))
			return
		}
		node, ok = tombstone.Obj.(*corev1.Node)
		if !ok {
			runtime.HandleError(fmt.Errorf("tombstone contained object that is not a Node %T", obj))
			return
		}
	}

	// Delete egress rules for this node
	ctx := context.Background()
	if err := c.options.Reconciler.DeleteNode(ctx, node.Name); err != nil {
		runtime.HandleError(fmt.Errorf("failed to delete egress rules for node %s: %w", node.Name, err))
	}
}

// podCIDRsChanged checks if pod CIDRs changed between old and new node
func podCIDRsChanged(oldNode, newNode *corev1.Node) bool {
	if len(oldNode.Spec.PodCIDRs) != len(newNode.Spec.PodCIDRs) {
		return true
	}

	for i := range oldNode.Spec.PodCIDRs {
		if oldNode.Spec.PodCIDRs[i] != newNode.Spec.PodCIDRs[i] {
			return true
		}
	}

	return false
}

// cleanupOrphanedEgresses builds a map of valid Netmaker node IDs from K8s nodes
// and calls the reconciler to clean up orphaned egress rules
//
// Race safety: This method reads from the informer cache (thread-safe) and calls
// Netmaker API methods. The Netmaker client operations are safe because:
// - ListNodes/ListEgress use caching with TTL (reads are eventually consistent)
// - DeleteEgress is idempotent (returns success even if already deleted)
// - The reconciler checks existence before creating/updating
//
// The worst-case race is deleting an egress rule that's being created concurrently,
// which will be recreated on the next reconciliation cycle (self-healing).
//
// Time complexity: O(n + m) where n = K8s nodes, m = Netmaker hosts
// Memory complexity: O(m) for hostname map + O(total node IDs) for validNodeIDs
func (c *Controller) cleanupOrphanedEgresses(ctx context.Context) error {
	// Build set of valid Netmaker node IDs from all K8s nodes
	validNodeIDs := make(map[string]bool)

	// List all Netmaker hosts once and build hostname->nodeIDs map for O(1) lookups
	// This is O(n + m) instead of O(n × m) if we called GetNodeIDsByHostname per node
	hosts, err := c.options.NetmakerClient.ListHosts(ctx)
	if err != nil {
		return fmt.Errorf("failed to list Netmaker hosts: %w", err)
	}

	hostnameToNodeIDs := make(map[string][]string, len(hosts))
	for _, host := range hosts {
		hostnameToNodeIDs[host.Name] = host.Nodes
	}

	// List all K8s nodes from informer cache (thread-safe read)
	nodeList := c.nodeInformer.GetIndexer().List()
	for _, obj := range nodeList {
		node, ok := obj.(*corev1.Node)
		if !ok {
			runtime.HandleError(fmt.Errorf("expected Node but got %T", obj))
			continue
		}

		// Skip nodes without pod CIDRs (not ready yet)
		if len(node.Spec.PodCIDRs) == 0 {
			continue
		}

		// O(1) map lookup instead of O(m) linear search
		nodeIDs, exists := hostnameToNodeIDs[node.Name]
		if !exists {
			// Host doesn't exist in Netmaker - skip silently
			continue
		}

		// Add all node IDs to the valid set
		for _, nodeID := range nodeIDs {
			validNodeIDs[nodeID] = true
		}
	}

	// Call reconciler to clean up orphaned egress rules
	return c.options.Reconciler.CleanupOrphanedEgresses(ctx, validNodeIDs)
}

// periodicCleanup is a wrapper for periodic cleanup execution
func (c *Controller) periodicCleanup(ctx context.Context) {
	if err := c.cleanupOrphanedEgresses(ctx); err != nil {
		runtime.HandleError(fmt.Errorf("periodic cleanup failed: %w", err))
	}
}
