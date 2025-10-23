# kaput-not Helm Chart

This Helm chart deploys kaput-not, a CNI-agnostic Kubernetes controller for managing Netmaker egress rules.

## Installation

### Prerequisites

- Kubernetes 1.19+
- Helm 3.8+ (for OCI registry support)
- Netmaker instance with API access
- Netmaker service account credentials

### Install the Chart

#### From GHCR (GitHub Container Registry)

```bash
helm install kaput-not oci://ghcr.io/bsure-analytics/charts/kaput-not \
  --namespace kube-system \
  --create-namespace \
  --set netmaker.apiUrl="https://api.netmaker.example.com" \
  --set netmaker.username="kaput-not" \
  --set netmaker.password="your-password-here"
```

For multi-cluster deployments (multiple K8s clusters sharing a Netmaker network):

```bash
helm install kaput-not oci://ghcr.io/bsure-analytics/charts/kaput-not \
  --namespace kube-system \
  --create-namespace \
  --set netmaker.apiUrl="https://api.netmaker.example.com" \
  --set netmaker.username="kaput-not" \
  --set netmaker.password="your-password-here" \
  --set clusterName="us-east"
```

To install a specific version:

```bash
helm install kaput-not oci://ghcr.io/bsure-analytics/charts/kaput-not --version 0.3.0 \
  --namespace kube-system \
  --create-namespace \
  --set netmaker.apiUrl="https://api.netmaker.example.com" \
  --set netmaker.username="kaput-not" \
  --set netmaker.password="your-password-here"
```

#### From Local Chart

```bash
helm install kaput-not ./charts/kaput-not \
  --namespace kube-system \
  --create-namespace \
  --set netmaker.apiUrl="https://api.netmaker.example.com" \
  --set netmaker.username="kaput-not" \
  --set netmaker.password="your-password-here"
```

### Using a Values File (Recommended)

Create a custom values file:

```yaml
# my-values.yaml
netmaker:
  apiUrl: "https://api.netmaker.example.com"
  username: "kaput-not"
  password: "your-secure-password"

replicaCount: 2

resources:
  requests:
    memory: "64Mi"
    cpu: "100m"
  limits:
    memory: "128Mi"
    cpu: "200m"
```

Install using the values file:

```bash
helm install kaput-not ./charts/kaput-not \
  --namespace kube-system \
  --create-namespace \
  -f my-values.yaml
```

## Configuration

See `values.yaml` for all available configuration options.

### Required Values

| Parameter | Description | Default |
|-----------|-------------|---------|
| `netmaker.apiUrl` | Netmaker API endpoint | `https://api.netmaker.example.com` |
| `netmaker.username` | Netmaker username | `kaput-not` |
| `netmaker.password` | Netmaker password | `REPLACE-WITH-ACTUAL-PASSWORD` |

**Networks are auto-discovered** from the Netmaker API based on which networks each Kubernetes host participates in.

### Optional Values

| Parameter | Description | Default |
|-----------|-------------|---------|
| `clusterName` | Cluster identifier for multi-cluster deployments | `""` (single-cluster mode) |
| `replicaCount` | Number of controller replicas | `2` |
| `image.repository` | Docker image repository | `ghcr.io/bsure-analytics/kaput-not` |
| `image.tag` | Docker image tag | Chart appVersion |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `leaderElection.enabled` | Enable leader election | `true` |
| `leaderElection.id` | Lease resource name | `kaput-not` |
| `resources.requests.cpu` | CPU request | `100m` |
| `resources.requests.memory` | Memory request | `64Mi` |
| `resources.limits.cpu` | CPU limit | `200m` |
| `resources.limits.memory` | Memory limit | `128Mi` |
| `priorityClassName` | Priority class for pod scheduling | `system-cluster-critical` |

**Note:** The leader election namespace is auto-detected from the pod's service account and does not need to be configured.

## Resource Scaling

kaput-not's memory usage scales linearly with the number of Kubernetes nodes in your cluster (O(n) complexity).

### Recommended Resource Limits by Cluster Size

| Cluster Size | Memory Request | Memory Limit |
|--------------|----------------|--------------|
| < 500 nodes | 64 Mi | 128 Mi |
| 500-1,000 nodes | 128 Mi | 256 Mi |
| 1,000-5,000 nodes | 256 Mi | 512 Mi |
| 5,000+ nodes | 512 Mi | 1 Gi |

### Scaling Example

For a 1,000-node cluster, create a custom values file:

```yaml
# values-large-cluster.yaml
resources:
  requests:
    memory: "256Mi"
    cpu: "100m"
  limits:
    memory: "512Mi"
    cpu: "200m"
```

Then install or upgrade:

```bash
helm upgrade kaput-not oci://ghcr.io/bsure-analytics/charts/kaput-not \
  --namespace kube-system \
  -f values-large-cluster.yaml
```

### Memory Usage Details

The primary memory consumer is the Kubernetes informer cache (~25 KB per node). A 100-node cluster typically uses 50-100 MB total memory. The default limits (64Mi/128Mi) are appropriate for most small to medium clusters.

## Features

### TTL-Based Caching

kaput-not includes intelligent caching to reduce API calls to Netmaker:

- **30-second default TTL** for cached responses
- Caches authentication tokens, host lookups, node lookups, and egress gateway lists
- Network-aware caching (separate entries per network)
- Automatic invalidation on TTL expiry and authentication failures
- Thread-safe for concurrent access

### Multi-Cluster Support

When multiple Kubernetes clusters share a single Netmaker network, use cluster name scoping to prevent conflicts:

**Single-cluster mode (default):**
```yaml
clusterName: ""  # Manages all kaput-not egress rules
```

**Multi-cluster mode:**
```yaml
clusterName: "us-east"  # Only manages egress rules with this cluster name
```

**How it works:**
- Egress rules include the cluster name in the description
- Single-cluster: `"Managed by kaput-not (DO NOT EDIT): index=0"`
- Multi-cluster: `"Managed by kaput-not (DO NOT EDIT): cluster=us-east index=0"`
- Each cluster manages only its own egress rules
- Migration safety: existing rules without cluster names are preserved

**Example multi-cluster deployment:**
```yaml
# values-us-east.yaml
netmaker:
  apiUrl: "https://api.netmaker.example.com"
  username: "kaput-not"
  password: "secure-password"
clusterName: "us-east"
```

```yaml
# values-eu-west.yaml
netmaker:
  apiUrl: "https://api.netmaker.example.com"
  username: "kaput-not"
  password: "secure-password"
clusterName: "eu-west"
```

### Multi-Network Support

kaput-not can manage a Kubernetes node across multiple Netmaker networks:

- Networks are auto-discovered from the Netmaker API
- Each network gets independent egress rules
- Useful for complex network topologies
- Cache is network-aware to prevent cross-network data leakage

## Upgrading

### From GHCR

```bash
helm upgrade kaput-not oci://ghcr.io/bsure-analytics/charts/kaput-not \
  --namespace kube-system \
  -f my-values.yaml
```

To upgrade to a specific version:

```bash
helm upgrade kaput-not oci://ghcr.io/bsure-analytics/charts/kaput-not --version 0.3.0 \
  --namespace kube-system \
  -f my-values.yaml
```

### From Local Chart

```bash
helm upgrade kaput-not ./charts/kaput-not \
  --namespace kube-system \
  -f my-values.yaml
```

### Update Specific Values

To update only specific values while keeping others:

```bash
helm upgrade kaput-not oci://ghcr.io/bsure-analytics/charts/kaput-not \
  --namespace kube-system \
  --reuse-values \
  --set image.tag="v0.2.0"
```

## Uninstalling

```bash
helm uninstall kaput-not --namespace kube-system
```

## Troubleshooting

### Check Pod Status

```bash
kubectl get pods -n kube-system -l app.kubernetes.io/name=kaput-not
```

### View Logs

```bash
kubectl logs -n kube-system -l app.kubernetes.io/name=kaput-not --tail=50
```

### Check Leader

```bash
kubectl get lease -n kube-system kaput-not -o yaml
```

### Verify Configuration

```bash
helm get values kaput-not --namespace kube-system
```

## Security

**Important:** Never commit credentials to git!

- Use `--set` flags for credentials during installation
- Or create a separate values file (add to `.gitignore`)
- Rotate credentials periodically
- Use strong, random passwords (32+ characters)
