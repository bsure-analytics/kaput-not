# kaput-not

**A CNI-agnostic Kubernetes controller for managing Netmaker egress rules**

*Note: "kaput-not" is a wordplay on the German word "kaputt" (broken), suggesting this controller keeps the overlay network from breaking.*

## The Problem

When running Kubernetes on WireGuard-based mesh networks like Netmaker, **your network will eventually break** without special preparation. The issue: private IPv4 addresses from pod CIDR ranges leak into WireGuard tunnels as endpoint IP addresses, but WireGuard endpoints must be publicly routable IPs.

**Symptom**: When the network breaks, running `sudo wg show all endpoints` on each node will show private IP addresses from peer node's pod CIDR ranges instead of their public IPv4 addresses.

**Triggers**: Common operations can break the network unexpectedly:
- Running `netclient pull` in a terminal on a node
- Clicking "Sync Device" or "Sync All" in the Netmaker dashboard
- Interface up/down events (network hiccups, node reboots, etc.)

**The Solution**: kaput-not automatically manages Netmaker egress rules to prevent this leakage by adding pod CIDR ranges to the WireGuard allowed-ips for each peer. This ensures pod traffic stays within the mesh and never gets mistaken for tunnel endpoints.

## Overview

kaput-not is a Kubernetes controller that automatically synchronizes pod CIDR allocations from Kubernetes nodes to Netmaker Egress gateway rules. It works with **any CNI** that populates the standard `spec.podCIDRs` field on Node resources (Cilium, Calico, Flannel, etc.).

### Key Features

- ✅ **CNI-Agnostic**: Works with any CNI using standard Kubernetes API
- ✅ **Leader Election**: High availability with multiple replicas
- ✅ **Rebuild Resilient**: Uses index-based lookup for stable egress rule management
- ✅ **Multi-CIDR Support**: Handles multiple pod CIDRs per node (IPv4/IPv6 dual-stack)
- ✅ **Minimal Permissions**: Read-only access to nodes, lease management only
- ✅ **Twelve-Factor App**: Configuration via environment variables
- ✅ **Small Footprint**: <20MB Docker image using distroless

## How It Works

1. **Watches** Kubernetes Node resources for pod CIDR changes
2. **Maps** Kubernetes node names to Netmaker node IDs
3. **Syncs** pod CIDRs to Netmaker Egress rules
4. **Maintains** stable egress rules using index-based lookup

### Egress Rule Format

Each pod CIDR gets its own egress rule with:
- **Description**: `Managed by kaput-not (DO NOT EDIT): index=0` (stable identifier)
- **Name**: `node-name pods (1/2)` (human-friendly)
- **Range**: Pod CIDR value (e.g., `10.160.0.0/24`)
- **NAT**: `false` (no source NAT for pod CIDRs)
- **Nodes**: Map containing the Netmaker node UUID (e.g., `{"uuid": 500}`)

The index-based description combined with the node ID in the nodes map ensures that egress rules survive pod CIDR changes while preventing orphaned rules.

## Installation

### Prerequisites

- Kubernetes cluster (1.19+)
- Helm 3.0+
- Netmaker instance with API access
- Netmaker service account (see setup below)

### Setting Up Netmaker Authentication

**Important:** kaput-not requires **Netmaker user credentials** (username/password), **not** enrollment keys.

#### Creating a Service Account

Create a dedicated Netmaker user for kaput-not:

**Option 1: Via Netmaker Dashboard (Recommended)**

1. Log into Netmaker Dashboard at `https://dashboard.netmaker.example.com`
2. Go to **Users** → **Add User**
3. Create user:
   - **Username**: `kaput-not`
   - **Password**: Generate strong password (e.g., `openssl rand -base64 32`)
   - **Role**: Grant node management and egress operations permissions
4. Save credentials securely

**Option 2: Via Netmaker API**

```bash
# Authenticate as admin
ADMIN_TOKEN=$(curl -s -X POST https://api.netmaker.example.com/api/users/adm/authenticate \
  -H "Content-Type: application/json" \
  -d '{"username": "admin", "password": "admin-password"}' \
  | jq -r '.Response.AuthToken')

# Create service account
curl -X POST https://api.netmaker.example.com/api/users \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "username": "kaput-not",
    "password": "strong-random-password"
  }'
```

#### Required Permissions

The service account must have permissions to:
- Read node information
- List, create, update, and delete egress gateways for the network

#### Security Best Practices

- ✅ Use dedicated service account (don't reuse admin credentials)
- ✅ Generate strong random passwords (32+ characters)
- ✅ Limit permissions to only node and egress management
- ✅ Store credentials in Kubernetes Secrets (never commit to git)
- ✅ Rotate credentials periodically

### Quick Start

#### Option 1: Install from GHCR (Recommended)

```bash
helm install kaput-not oci://ghcr.io/bsure-analytics/kaput-not \
  --namespace kube-system \
  --create-namespace \
  --set netmaker.apiUrl="https://api.netmaker.example.com" \
  --set netmaker.username="kaput-not" \
  --set netmaker.password="your-password-here"
```

#### Option 2: Install from Local Chart

```bash
helm install kaput-not ./charts/kaput-not \
  --namespace kube-system \
  --create-namespace \
  --set netmaker.apiUrl="https://api.netmaker.example.com" \
  --set netmaker.username="kaput-not" \
  --set netmaker.password="your-password-here"
```

**Recommended: Use a separate values file for credentials** (never commit to git):

```bash
# Create my-values.yaml with your configuration
cat > my-values.yaml <<EOF
netmaker:
  apiUrl: "https://api.netmaker.example.com"
  username: "kaput-not"
  password: "your-secure-password"
EOF

# Install from GHCR using the values file
helm install kaput-not oci://ghcr.io/bsure-analytics/kaput-not \
  --namespace kube-system \
  --create-namespace \
  -f my-values.yaml

# Or install from local chart
helm install kaput-not ./charts/kaput-not \
  --namespace kube-system \
  --create-namespace \
  -f my-values.yaml
```

2. **Verify deployment**:

```bash
kubectl get pods -n kube-system -l app.kubernetes.io/name=kaput-not
kubectl logs -n kube-system -l app.kubernetes.io/name=kaput-not --tail=50
```

3. **Check which pod is the leader**:

```bash
kubectl get lease -n kube-system kaput-not -o yaml
```

## Configuration

Configuration is managed via Helm values. See `charts/kaput-not/values.yaml` for all available options.

### Required Values

- `netmaker.apiUrl`: Netmaker API endpoint (e.g., `https://api.netmaker.example.com`)
- `netmaker.username`: Netmaker username for authentication
- `netmaker.password`: Netmaker password for authentication

**Networks are auto-discovered** from the Netmaker API based on which networks each Kubernetes host participates in.

### Common Optional Values

- `replicaCount`: Number of controller replicas (default: `2`)
- `image.repository`: Docker image repository (default: `ghcr.io/bsure-analytics/kaput-not`)
- `image.tag`: Docker image tag (default: chart appVersion)
- `leaderElection.enabled`: Enable leader election (default: `true`)
- `resources.requests/limits`: CPU and memory resources

### Example: Custom Configuration

```bash
# Using GHCR
helm upgrade kaput-not oci://ghcr.io/bsure-analytics/kaput-not \
  --namespace kube-system \
  --set replicaCount=3 \
  --set resources.requests.memory="128Mi" \
  --set image.tag="v1.0.0"

# Or using local chart
helm upgrade kaput-not ./charts/kaput-not \
  --namespace kube-system \
  --set replicaCount=3 \
  --set resources.requests.memory="128Mi" \
  --set image.tag="v1.0.0"
```

### Environment Variables (Local Development)

For local development without Helm:

**Required:**
- `KUBECONFIG`: Path to kubeconfig (for local development)
- `NETMAKER_API_URL`: Netmaker API endpoint
- `NETMAKER_USERNAME`: Netmaker username
- `NETMAKER_PASSWORD`: Netmaker password

**Networks are auto-discovered** from the Netmaker API based on which networks each Kubernetes host participates in.

**Optional (auto-detected):**
- `LEADER_ELECTION_ENABLED`: Enable leader election (auto-detected: disabled for local dev, enabled in-cluster)
- `LEADER_ELECTION_NAMESPACE`: Namespace for lease resource (auto-detected: pod's namespace in-cluster, `kube-system` for local dev)
- `LEADER_ELECTION_ID`: Lease resource name (default: `kaput-not`)

## Architecture

kaput-not follows **Hexagonal Architecture** (Ports & Adapters):

```
cmd/kaput-not/          # CLI adapter ("let it crash")
  ├── main.go           # Entry point, panics on errors
  └── config.go         # Environment variable loading

pkg/                    # Library (pure business logic)
  ├── netmaker/         # Netmaker API client with TTL-based caching
  ├── reconciler/       # Reconciliation logic
  ├── controller/       # Kubernetes controller (informer)
  └── leaderelection/   # Leader election logic

charts/kaput-not/       # Helm chart
  ├── Chart.yaml        # Chart metadata
  ├── values.yaml       # Default configuration values
  └── templates/        # Kubernetes resource templates
```

### Design Principles

- **KISS**: No retry logic, no state persistence, let it crash and restart
- **DRY**: Shared reconciliation function for CREATE/UPDATE
- **Twelve-Factor**: Configuration via environment only
- **Library + Adapter**: Business logic returns errors, CLI adapter panics
- **Intelligent Caching**: TTL-based caching (30 second default) reduces API calls to Netmaker while maintaining consistency

### Caching Layer

kaput-not includes a TTL-based caching layer that significantly reduces API calls to Netmaker:

- **Default TTL**: 30 seconds for all cached responses
- **What's cached**: Authentication tokens, host lookups, node lookups, and egress gateway lists
- **Network-aware**: Separate cache entries per Netmaker network
- **Thread-safe**: Uses mutex locks for concurrent access
- **Auto-invalidation**: Expires on TTL timeout and authentication failures
- **Transparent**: No code changes needed - caching happens automatically in the HTTP client

This reduces load on the Netmaker API while maintaining near real-time consistency, especially important during the periodic 10-minute resync cycles.

### Multi-Network Support

kaput-not supports managing a Kubernetes node across multiple Netmaker networks simultaneously:

- Specify multiple networks via comma or space separation: `"network1,network2"` or `"network1 network2"`
- Each network gets independent egress rules with index-based management
- Useful for complex topologies where nodes participate in multiple mesh networks
- Cache is network-aware to prevent cross-network data leakage

## High Availability

kaput-not supports high availability through Kubernetes lease-based leader election:

- **2 replicas** (configurable via deployment)
- **Only one active** controller at a time
- **Automatic failover** if leader fails
- **No split-brain** due to lease-based locking

Check which replica is the leader:

```bash
kubectl get lease -n kube-system kaput-not -o yaml
```

## Resource Requirements and Scaling

kaput-not has **O(n) memory complexity** where n is the number of Kubernetes nodes.

### Memory Usage by Cluster Size

| Cluster Size | Memory Request | Memory Limit | Notes |
|--------------|----------------|--------------|-------|
| < 500 nodes | 64 Mi | 128 Mi | Default configuration |
| 500-1,000 nodes | 128 Mi | 256 Mi | Recommended for medium clusters |
| 1,000-5,000 nodes | 256 Mi | 512 Mi | Recommended for large clusters |
| 5,000+ nodes | 512 Mi | 1 Gi | Recommended for very large clusters |

### What Drives Memory Usage

The primary memory consumer is the **Kubernetes informer cache**, which stores a complete copy of all Node objects:

- **Per-node overhead**: ~25 KB (metadata + spec + status)
- **Workqueue**: Negligible (transient, usually empty)
- **Application code**: ~10-50 MB (Go runtime + libraries)

**Example**: A 100-node cluster uses approximately 50-100 MB total memory.

### Adjusting Resource Limits

Update resource limits in your Helm values:

```yaml
resources:
  requests:
    memory: "128Mi"
    cpu: "100m"
  limits:
    memory: "256Mi"
    cpu: "200m"
```

### Event Processing

The controller provides both real-time and periodic reconciliation:

- ✅ **Immediate response** to Node events (add/update/delete via Kubernetes watch API)
- ✅ **Full reconciliation** on startup (syncs all existing nodes)
- ✅ **Periodic resync** every 10 minutes (drift correction, no action if pod CIDRs unchanged)

This ensures consistency after downtime and corrects any manual changes to Netmaker egress rules.

## Local Development

### Prerequisites

- Go 1.25+
- Docker (for building images)
- kubectl and access to a Kubernetes cluster

### Building

```bash
# Build binary
make build

# Run tests
make test

# Run linters
make lint

# Build Docker image
make docker-build

# Lint Helm chart
make helm-lint

# Package Helm chart
make helm-package
```

### Running Locally

Set environment variables and run:

```bash
export NETMAKER_API_URL="https://api.netmaker.example.com"
export NETMAKER_USERNAME="kaput-not"
export NETMAKER_PASSWORD="your-password"
export KUBECONFIG="$HOME/.kube/config"

# Networks are auto-discovered from Netmaker API
./bin/kaput-not
```

**Note:** Leader election is automatically disabled when running locally (not in-cluster).

## Troubleshooting

### Authentication failures

**Symptom**: Logs show `authentication failed with HTTP status 401`

**Solutions**:

1. Verify credentials in Secret:
   ```bash
   kubectl get secret kaput-not -n kube-system -o jsonpath='{.data.NETMAKER_USERNAME}' | base64 -d
   ```

2. Test credentials manually:
   ```bash
   curl -X POST https://api.netmaker.example.com/api/users/adm/authenticate \
     -H "Content-Type: application/json" \
     -d '{
       "username": "kaput-not",
       "password": "your-password"
     }'
   # Should return AuthToken if valid
   ```

3. Verify user exists in Netmaker Dashboard → Users

4. If password changed, upgrade Helm release with new password:
   ```bash
   # Using GHCR
   helm upgrade kaput-not oci://ghcr.io/bsure-analytics/kaput-not \
     --namespace kube-system \
     --reuse-values \
     --set netmaker.password="<new-password>"

   # Or using local chart
   helm upgrade kaput-not ./charts/kaput-not \
     --namespace kube-system \
     --reuse-values \
     --set netmaker.password="<new-password>"
   ```

### Controller not starting

```bash
# Check logs
kubectl logs -n kube-system -l app.kubernetes.io/name=kaput-not --tail=100

# Common issues:
# 1. Wrong configuration - verify Helm values
# 2. Wrong API URL - check netmaker.apiUrl value
# 3. Authentication failed - see "Authentication failures" above
```

### Egress rules not created

```bash
# Check if node has pod CIDRs assigned
kubectl get node <node-name> -o jsonpath='{.spec.podCIDRs}'

# Check controller logs for reconciliation errors
kubectl logs -n kube-system -l app.kubernetes.io/name=kaput-not | grep "failed to reconcile"

# Verify Netmaker host exists with matching name
curl -H "Authorization: Bearer $TOKEN" \
  https://api.netmaker.example.com/api/hosts | jq '.[] | select(.name=="node-name")'
```

### Multiple leaders / split-brain

```bash
# Check lease status
kubectl get lease -n kube-system kaput-not -o yaml

# Verify only one pod is active leader (check logs)
kubectl logs -n kube-system -l app.kubernetes.io/name=kaput-not | grep "Became leader"
```

### Pod CIDR changed but egress not updated

The controller should detect this automatically. If not:

```bash
# Force reconciliation by restarting the controller
kubectl rollout restart deployment -n kube-system -l app.kubernetes.io/name=kaput-not

# Check if the egress rule exists with old CIDR
curl -H "Authorization: Bearer $TOKEN" \
  "https://api.netmaker.example.com/api/v1/egress?network=development" | \
  jq '.Response[] | select(.description | contains("Managed by kaput-not"))'
```

## Contributing

Contributions are welcome! Please:

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

Copyright © 2025 bsure-analytics

Licensed under the Apache License, Version 2.0. See LICENSE file for details.

## Published Artifacts

kaput-not releases are automatically published to GitHub Container Registry (GHCR):

- **Docker Images**: `ghcr.io/bsure-analytics/kaput-not`
  - View at: https://github.com/bsure-analytics/kaput-not/pkgs/container/kaput-not
  - Tags: latest, version numbers (1.0.0, 1.0, 1), branch names, git SHA

- **Helm Charts**: `oci://ghcr.io/bsure-analytics/kaput-not`
  - Published as OCI artifacts (Helm 3.8+ required)
  - Version numbers match git tags (v1.0.0 → chart version 1.0.0)
  - Install: `helm install kaput-not oci://ghcr.io/bsure-analytics/kaput-not`

## Related Projects

- [Netmaker](https://github.com/gravitl/netmaker) - WireGuard mesh network platform
