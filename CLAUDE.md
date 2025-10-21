# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

kaput-not is a Kubernetes controller that automatically synchronizes pod CIDR allocations from Kubernetes nodes to Netmaker egress gateway rules. It's CNI-agnostic and works with any CNI that populates the standard `spec.podCIDRs` field on Node resources.

## Architecture

### Hexagonal Architecture (Ports & Adapters)

The project follows a clean separation between business logic and infrastructure:

**Library Layer (`pkg/`)** - Pure business logic, returns errors, never panics:
- `pkg/netmaker/` - Netmaker API client with minimal types (only fields we actually use) and TTL-based caching
- `pkg/reconciler/` - Reconciliation logic for syncing pod CIDRs to egress rules across multiple networks
- `pkg/controller/` - Kubernetes controller (informer pattern, workqueue)
- `pkg/leaderelection/` - Kubernetes lease-based leader election for HA

**CLI Adapter (`cmd/kaput-not/`)** - Infrastructure layer, "let it crash" philosophy:
- `main.go` - Entry point, converts library errors to panics
- `config.go` - Environment variable loading (twelve-factor app)

### Key Design Patterns

**Index-Based Egress Rule Management:**
- Egress rules use `description` field: `"Managed by kaput-not (DO NOT EDIT): index=0"`
- Node ID is stored in the `nodes` map, not in description (avoid redundancy)
- Lookup requires matching BOTH description index AND node ID in nodes map
- This allows pod CIDRs to change over time without orphaning egress rules

**Netmaker Node ID Mapping:**
- Two-step lookup: K8s node name → Netmaker host → Netmaker node UUID
- `ListHosts()` finds host by matching `name` field with K8s node name
- `ListNodes()` finds node by matching `hostid` field
- The node UUID goes in egress rule's `nodes` map

**Error Handling - Three Levels:**
1. HTTP status code check
2. Content-Type header validation (must be `application/json`)
3. JSON `Code` field check (if present in response)

All Netmaker API methods follow this pattern for robust error detection.

**Minimal Type Definitions:**
- Types only include fields we actually use (resilient to Netmaker API changes)
- `Code` and `Message` fields marked as `omitempty` (for error handling only)
- Unknown fields from API responses are silently ignored by Go's json decoder

**TTL-Based Caching Layer:**
- Reduces API calls to Netmaker by caching responses for a configurable TTL (default 30 seconds)
- Caches authentication tokens, host lookups, node lookups, and egress gateway lists
- Each cache entry has independent TTL tracking
- Transparent to callers - caching happens automatically in the HTTP client layer
- Thread-safe using mutex locks for concurrent access
- Cache automatically invalidates on TTL expiry and authentication failures

**Automatic Network Discovery:**
- Networks are auto-discovered from the Netmaker API - no manual configuration required
- Each Kubernetes node maps to a Netmaker host, which has a `nodes` array containing all node UUIDs
- Each Netmaker node contains a `network` field indicating which network it belongs to
- The reconciler gets all node IDs from the host, then matches them with nodes to discover networks
- A single Kubernetes node can participate in multiple Netmaker networks simultaneously
- Each network gets independent egress rules with the same index-based management approach
- Optimized API calls: GetNodeIDsByHostname() and ListNodes() called once per reconciliation, not per-network
- Useful for complex topologies where nodes participate in multiple mesh networks

## Common Development Tasks

### Build and Test

```bash
# Build binary (output to bin/kaput-not)
make build

# Run tests with race detection and coverage
make test

# View coverage report
make test-coverage

# Run linters (uses .golangci.yaml config)
make lint

# Format code
make fmt
```

### Run Locally

Requires minimal environment variables (leader election auto-disabled):

```bash
export NETMAKER_API_URL="https://api.netmaker.example.com"
export NETMAKER_USERNAME="kaput-not"
export NETMAKER_PASSWORD="your-password"
export KUBECONFIG="$HOME/.kube/config"

# Networks are auto-discovered from Netmaker API
make run
```

**Note:** Leader election is automatically disabled for local development (when not running in-cluster).

### Docker

```bash
# Build Docker image
make docker-build

# Override image name/version
DOCKER_IMAGE=myrepo/kaput-not VERSION=v1.0.0 make docker-build
```

### Deploy to Kubernetes

```bash
# Install from GHCR (recommended)
helm install kaput-not oci://ghcr.io/bsure-analytics/kaput-not \
  --namespace kube-system \
  --set netmaker.apiUrl="https://api.netmaker.example.com" \
  --set netmaker.username="kaput-not" \
  --set netmaker.password="secret"

# Install from local chart
helm install kaput-not ./charts/kaput-not \
  --namespace kube-system \
  --set netmaker.apiUrl="https://api.netmaker.example.com" \
  --set netmaker.username="kaput-not" \
  --set netmaker.password="secret"

# Deploy using Makefile (uses local chart)
make deploy  # Alias for helm-upgrade

# Upgrade release
make helm-upgrade

# Uninstall
make undeploy  # Alias for helm-uninstall

# Lint Helm chart
make helm-lint

# Render templates (dry-run)
make helm-template

# Package chart for distribution
make helm-package
```

## Code Modification Guidelines

### When Adding Netmaker API Fields

Only add fields to types (`pkg/netmaker/types.go`) if you actually use them in the code. This keeps the codebase resilient to API changes.

**Example - Don't do this:**
```
type Host struct {
    ID       string   `json:"id"`
    Name     string   `json:"name"`
    OS       string   `json:"os"`        // Unused field
    Version  string   `json:"version"`   // Unused field
    // ... 20 more unused fields
}
```

**Example - Do this:**
```
type Host struct {
    ID    string   `json:"id"`
    Name  string   `json:"name"`
    Nodes []string `json:"nodes,omitempty"`
}
// Unknown fields from API are silently ignored
```

### When Adding New Netmaker API Calls

Follow the three-level error checking pattern:

```
func (c *HTTPClient) NewMethod(ctx context.Context) error {
    resp, err := c.doRequest(ctx, http.MethodGet, url, nil)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    // Level 1: HTTP status
    if resp.StatusCode != http.StatusOK {
        bodyBytes, _ := io.ReadAll(resp.Body)
        return fmt.Errorf("NewMethod failed with HTTP status %d: %s", resp.StatusCode, string(bodyBytes))
    }

    // Level 2: Content-Type
    contentType := resp.Header.Get("Content-Type")
    if !strings.Contains(contentType, "application/json") {
        return fmt.Errorf("expected JSON response, got Content-Type: %s", contentType)
    }

    var response SomeResponse
    if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
        return fmt.Errorf("failed to decode response: %w", err)
    }

    // Level 3: JSON Code field (if present)
    if response.Code != 0 && response.Code != http.StatusOK {
        return fmt.Errorf("NewMethod failed with API code %d: %s", response.Code, response.Message)
    }

    return nil
}
```

### Reconciliation Logic

The reconciler (`pkg/reconciler/reconciler.go`) handles the core business logic:

- `ReconcileNode()` - Syncs all pod CIDRs for a node to Netmaker
- `reconcilePodCIDR()` - Handles individual CIDR (find existing by index + node ID, create or update)
- `DeleteNode()` - Removes all egress rules for a deleted node

When modifying reconciliation:
- Always check if egress already exists before creating
- Use composite lookup: description index AND node ID in nodes map
- Update existing egress if only the CIDR value changed
- Use `EgressMetric = 500` as the metric value for nodes map

### Controller Event Handlers

The controller (`pkg/controller/controller.go`) uses the informer pattern:

- `handleNodeAdd()` - Enqueues node for reconciliation
- `handleNodeUpdate()` - Only enqueues if `podCIDRsChanged()` returns true
- `handleNodeDelete()` - Directly calls reconciler's `DeleteNode()`

Don't reconcile on every update - check if pod CIDRs actually changed.

## Configuration

All configuration is via environment variables (twelve-factor app):

**Required:**
- `NETMAKER_API_URL` - Netmaker API endpoint
- `NETMAKER_USERNAME` - Service account username
- `NETMAKER_PASSWORD` - Service account password

**Networks are auto-discovered** from the Netmaker API based on which networks each Kubernetes host participates in:
  - Queries GET /api/hosts to find the host and get its node IDs (from the `nodes` array)
  - Queries GET /api/nodes to get all nodes across all networks
  - Filters nodes by matching node IDs - each node contains its network in the `network` field
  - Allows a Kubernetes node to be managed across multiple Netmaker networks simultaneously
  - Each network gets independent egress rules managed with the same index-based approach

**Optional (with auto-detection):**
- `KUBECONFIG` - Path to kubeconfig (empty = in-cluster mode)
- `LEADER_ELECTION_ENABLED` - Enable leader election (auto-detected: disabled for local, enabled in-cluster)
- `LEADER_ELECTION_NAMESPACE` - Namespace for lease (auto-detected: pod's namespace in-cluster, "kube-system" for local)
- `LEADER_ELECTION_ID` - Lease resource name (default: kaput-not)

**Auto-detection logic:**
- In-cluster detection: checks for `/var/run/secrets/kubernetes.io/serviceaccount/namespace` file
- When in-cluster: leader election enabled, namespace read from service account
- When local: leader election disabled, namespace defaults to "kube-system"

## Deployment

**Helm Chart:**
- Located in `charts/kaput-not/`
- Configurable via `values.yaml`
- Templates for ServiceAccount, ClusterRole, ClusterRoleBinding, ConfigMap, Secret, Deployment

**High Availability:**
- Runs 2 replicas (configurable) with Kubernetes lease-based leader election
- Only one replica is active (leader), the other is standby
- Automatic failover if leader fails
- No split-brain due to lease locking

**RBAC:**
- Read-only access to Nodes (core API)
- Manage leases in coordination.k8s.io (for leader election)

**Security:**
- Runs as non-root user (65532)
- Read-only root filesystem
- Drops all capabilities
- Distroless base image (~20MB)

**Configuration:**
- All settings via Helm values (netmaker.apiUrl, netmaker.username, netmaker.password)
- Secrets managed via Helm (can override with --set or values file)
- Never commit credentials to git
- Networks auto-discovered from Netmaker API (no manual configuration)

## Memory Complexity and Scaling

### Complexity Analysis: O(n)

Memory usage scales linearly with the number of Kubernetes nodes (n):

**Primary memory consumers:**
1. **Informer cache** (dominant): O(n) × ~25 KB per node
   - Stores complete Node objects (metadata, spec, status)
   - Read-only cache maintained by client-go
   - Updated via watch API (no polling)

2. **Workqueue**: O(1) in steady state
   - Transient storage of node names pending reconciliation
   - Usually empty (items processed within seconds)
   - Negligible memory footprint

3. **Application code**: O(1)
   - Go runtime, libraries, goroutines: ~10-50 MB
   - HTTP client with TTL-based caching (minimal overhead: cached responses expire after 30s)
   - Stateless reconciler (no persistent in-memory state)

### Memory Estimates by Cluster Size

| Nodes | Informer Cache | Total Memory | Helm Defaults Sufficient? |
|-------|----------------|--------------|---------------------------|
| 10 | ~250 KB | 50-80 MB | ✅ Yes (64Mi/128Mi) |
| 100 | ~2.5 MB | 50-100 MB | ✅ Yes (64Mi/128Mi) |
| 500 | ~12.5 MB | 75-125 MB | ⚠️ Marginal (increase to 128Mi/256Mi) |
| 1,000 | ~25 MB | 100-150 MB | ❌ No (use 256Mi/512Mi) |
| 5,000 | ~125 MB | 200-250 MB | ❌ No (use 512Mi/1Gi) |
| 10,000 | ~250 MB | 300-400 MB | ❌ No (use 512Mi/1Gi) |

### Event Processing Architecture

The controller provides three layers of synchronization:

1. **Real-time events** (primary mechanism):
   - Add/Update/Delete events via Kubernetes watch API
   - Immediate response (< 1 second latency)
   - Smart filtering: only processes updates if pod CIDRs changed

2. **Initial sync** (on startup):
   - Informer performs LIST operation for all nodes
   - Each node triggers `handleNodeAdd` event
   - Full reconciliation within minutes (depends on cluster size)

3. **Periodic resync** (every 10 minutes, configurable):
   - Re-lists all nodes to detect drift
   - Only reconciles if pod CIDRs actually changed
   - Provides safety net for manual Netmaker changes

### Performance Characteristics

**Startup time complexity:**
- Initial LIST: O(n) API call
- Reconciliation: O(n) Netmaker API calls (batched by node)
- Total startup time: ~1-5 minutes for 1000 nodes

**Steady-state complexity:**
- Per-event processing: O(1) (single node reconciliation)
- Memory overhead: O(n) (informer cache only)
- CPU usage: Near-zero when idle (event-driven)

**Resync period (default: 10 minutes):**
- Not configurable via environment/Helm (hardcoded in `pkg/controller/options.go:50`)
- Can be exposed if needed, but current default is sensible
- Smart: no-op if pod CIDRs unchanged since last sync

### Code References

- Informer cache: `pkg/controller/controller.go:33-37`
- Workqueue: `pkg/controller/controller.go:40`
- Resync period default: `pkg/controller/options.go:49-51`
- Event handlers: `pkg/controller/controller.go:49-53`
- Initial sync: `pkg/controller/controller.go:67-68`
- Smart update filtering: `pkg/controller/controller.go:179`

## CI/CD

GitHub Actions workflow (`.github/workflows/ci.yaml`):
1. **Test job** - Runs tests, linters, and builds binary
2. **Docker job** - Builds and pushes multi-platform images (amd64/arm64) to GHCR
3. **Helm job** - Packages and pushes Helm chart to GHCR OCI registry

**Docker Images** are automatically tagged based on:
- Branch names
- PR numbers
- Semver tags (v1.0.0 → latest, 1.0, 1)
- Git SHA
- `latest` for main branch

**Helm Charts** are published to GHCR OCI registry:
- Chart version matches git tag (v1.2.3 → chart version 1.2.3)
- Published on all push events (not PRs)
- Available at: `oci://ghcr.io/bsure-analytics/kaput-not`
- Install with: `helm install kaput-not oci://ghcr.io/bsure-analytics/kaput-not`

## Troubleshooting Development Issues

**Binary won't build:**
- Run `go mod tidy` to sync dependencies
- Check Go version is 1.25+

**Tests failing:**
- Ensure no leftover test files or mocks interfering
- Use `go test -v ./pkg/...` to run only pkg tests

**Linter errors:**
- Configuration is in `.golangci.yaml`
- Run `make fmt` before `make lint`

**Local run fails with authentication:**
- Verify `NETMAKER_USERNAME` and `NETMAKER_PASSWORD` are correct
- Test credentials: `curl -X POST $NETMAKER_API_URL/api/users/adm/authenticate -H "Content-Type: application/json" -d '{"username":"...","password":"..."}'`
- Check user exists in Netmaker Dashboard → Users
