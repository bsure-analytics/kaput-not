package netmaker

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// CachedClient decorates a Netmaker client with TTL-based caching
// Uses Go's interface embedding for automatic delegation (GoF Decorator pattern)
// Only methods we explicitly implement are overridden; rest delegate automatically
type CachedClient struct {
	Client // Embedded interface - automatic delegation

	mu sync.RWMutex

	// Host cache (global, not per-network)
	hosts          []Host
	hostsFetchedAt time.Time

	// Nodes cache (global)
	nodes          []Node
	nodesFetchedAt time.Time

	// Per-network caches
	egressByNetwork map[string][]Egress
	egressFetchedAt map[string]time.Time

	ttl time.Duration
}

// NewCachedClient wraps a client with TTL-based caching
// Default TTL is 30 seconds if ttl is 0
func NewCachedClient(client Client, ttl time.Duration) *CachedClient {
	if ttl == 0 {
		ttl = 30 * time.Second
	}

	return &CachedClient{
		Client:          client, // Embedded interface
		egressByNetwork: make(map[string][]Egress),
		egressFetchedAt: make(map[string]time.Time),
		ttl:             ttl,
	}
}

// Authenticate is not overridden - automatically delegates to embedded Client
// (No caching needed for authentication)

// ListHosts returns cached hosts or fetches fresh data if cache is stale
func (c *CachedClient) ListHosts(ctx context.Context) ([]Host, error) {
	// Fast path: check cache with read lock
	c.mu.RLock()
	if time.Since(c.hostsFetchedAt) < c.ttl {
		hosts := c.hosts
		c.mu.RUnlock()
		return hosts, nil
	}
	c.mu.RUnlock()

	// Cache miss - acquire write lock
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-checked locking: another goroutine might have fetched while we waited
	if time.Since(c.hostsFetchedAt) < c.ttl {
		return c.hosts, nil
	}

	// Fetch fresh data
	hosts, err := c.Client.ListHosts(ctx)
	if err != nil {
		return nil, err
	}

	// Update cache
	c.hosts = hosts
	c.hostsFetchedAt = time.Now()

	return hosts, nil
}

// ListNodes returns cached nodes data or fetches fresh if cache is stale
func (c *CachedClient) ListNodes(ctx context.Context) ([]Node, error) {
	// Fast path: check cache with read lock
	c.mu.RLock()
	if time.Since(c.nodesFetchedAt) < c.ttl {
		nodes := c.nodes
		c.mu.RUnlock()
		return nodes, nil
	}
	c.mu.RUnlock()

	// Cache miss - acquire write lock
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-checked locking
	if time.Since(c.nodesFetchedAt) < c.ttl {
		return c.nodes, nil
	}

	// Fetch fresh data
	nodes, err := c.Client.ListNodes(ctx)
	if err != nil {
		return nil, err
	}

	// Update cache
	c.nodes = nodes
	c.nodesFetchedAt = time.Now()

	return nodes, nil
}

// GetNodeIDsByHostname returns all Netmaker node IDs for a host by matching the hostname
// This is a CachedClient-specific helper method (not part of the Client interface)
// It uses cached ListHosts() to get node IDs directly from the host.Nodes field
func (c *CachedClient) GetNodeIDsByHostname(ctx context.Context, hostname string) ([]string, error) {
	// Get host by name (uses cache)
	hosts, err := c.ListHosts(ctx)
	if err != nil {
		return nil, err
	}

	for _, host := range hosts {
		if host.Name == hostname {
			return host.Nodes, nil
		}
	}

	return nil, fmt.Errorf("host not found with name %s", hostname)
}

// ListEgress returns cached egress rules or fetches fresh data if cache is stale
func (c *CachedClient) ListEgress(ctx context.Context, network string) ([]Egress, error) {
	// Fast path: check cache with read lock
	c.mu.RLock()
	if fetchedAt, exists := c.egressFetchedAt[network]; exists {
		if time.Since(fetchedAt) < c.ttl {
			egresses := c.egressByNetwork[network]
			c.mu.RUnlock()
			return egresses, nil
		}
	}
	c.mu.RUnlock()

	// Cache miss - acquire write lock
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-checked locking
	if fetchedAt, exists := c.egressFetchedAt[network]; exists {
		if time.Since(fetchedAt) < c.ttl {
			return c.egressByNetwork[network], nil
		}
	}

	// Fetch fresh data
	egresses, err := c.Client.ListEgress(ctx, network)
	if err != nil {
		return nil, err
	}

	// Update cache
	c.egressByNetwork[network] = egresses
	c.egressFetchedAt[network] = time.Now()

	return egresses, nil
}

// CreateEgress invalidates cache and delegates to underlying client
func (c *CachedClient) CreateEgress(ctx context.Context, req EgressReq) (*Egress, error) {
	egress, err := c.Client.CreateEgress(ctx, req)
	if err != nil {
		return nil, err
	}

	// Invalidate egress cache for this network
	c.mu.Lock()
	delete(c.egressByNetwork, req.Network)
	delete(c.egressFetchedAt, req.Network)
	c.mu.Unlock()

	return egress, nil
}

// UpdateEgress invalidates cache and delegates to underlying client
func (c *CachedClient) UpdateEgress(ctx context.Context, req EgressReq) (*Egress, error) {
	egress, err := c.Client.UpdateEgress(ctx, req)
	if err != nil {
		return nil, err
	}

	// Invalidate egress cache for this network
	c.mu.Lock()
	delete(c.egressByNetwork, req.Network)
	delete(c.egressFetchedAt, req.Network)
	c.mu.Unlock()

	return egress, nil
}

// DeleteEgress invalidates cache and delegates to underlying client
func (c *CachedClient) DeleteEgress(ctx context.Context, egressID string) error {
	err := c.Client.DeleteEgress(ctx, egressID)
	if err != nil {
		return err
	}

	// Invalidate ALL egress caches (we don't know which network the egress belongs to)
	c.mu.Lock()
	c.egressByNetwork = make(map[string][]Egress)
	c.egressFetchedAt = make(map[string]time.Time)
	c.mu.Unlock()

	return nil
}
