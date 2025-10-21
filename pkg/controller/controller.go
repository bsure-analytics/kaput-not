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
	workqueue    workqueue.RateLimitingInterface
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
	workqueue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	c := &Controller{
		options:      opts,
		nodeInformer: nodeInformerFactory,
		workqueue:    workqueue,
	}

	// Register event handlers
	c.nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.handleNodeAdd,
		UpdateFunc: c.handleNodeUpdate,
		DeleteFunc: c.handleNodeDelete,
	})

	return c, nil
}

// Run starts the controller and blocks until the context is cancelled
func (c *Controller) Run(ctx context.Context) error {
	defer runtime.HandleCrash()
	defer c.workqueue.ShutDown()

	// Start the informer
	go c.nodeInformer.Run(ctx.Done())

	// Wait for cache to sync
	if !cache.WaitForCacheSync(ctx.Done(), c.nodeInformer.HasSynced) {
		return fmt.Errorf("failed to wait for cache sync")
	}

	// Start workers
	for i := 0; i < c.options.WorkerCount; i++ {
		go wait.UntilWithContext(ctx, c.runWorker, time.Second)
	}

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
	obj, shutdown := c.workqueue.Get()
	if shutdown {
		return false
	}

	defer c.workqueue.Done(obj)

	var key string
	var ok bool
	if key, ok = obj.(string); !ok {
		c.workqueue.Forget(obj)
		runtime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
		return true
	}

	if err := c.syncHandler(ctx, key); err != nil {
		c.workqueue.AddRateLimited(key)
		runtime.HandleError(fmt.Errorf("error syncing '%s': %w, requeuing", key, err))
		return true
	}

	c.workqueue.Forget(obj)
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
