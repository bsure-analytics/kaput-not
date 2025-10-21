package netmaker

// AuthRequest is the request payload for authentication
type AuthRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// AuthResponse is the response from POST /api/users/adm/authenticate
// Code and Message are used for error handling
type AuthResponse struct {
	Code     int    `json:"Code,omitempty"`
	Message  string `json:"Message,omitempty"`
	Response struct {
		AuthToken string `json:"AuthToken"`
	} `json:"Response"`
}

// Host represents a Netmaker host - minimal fields for node lookup
// Unknown fields from the API are silently ignored
type Host struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`            // Matches Kubernetes node name
	Nodes []string `json:"nodes,omitempty"` // Array of node UUIDs
}

// Node represents a Netmaker node - minimal fields for host mapping
// Unknown fields from the API are silently ignored
type Node struct {
	ID      string `json:"id"`      // Node UUID
	HostID  string `json:"hostid"`  // Parent host UUID
	Network string `json:"network"` // Network this node belongs to
}

// EgressResponse is the response from GET /api/v1/egress?network={network}
// Code and Message are used for error handling
type EgressResponse struct {
	Code     int      `json:"Code,omitempty"`
	Message  string   `json:"Message,omitempty"`
	Response []Egress `json:"Response"`
}

// Egress represents a Netmaker egress gateway
// Only includes fields we actually use - unknown fields are silently ignored
type Egress struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Network     string         `json:"network"`
	Description string         `json:"description,omitempty"` // Contains our index key
	Range       string         `json:"range,omitempty"`       // Pod CIDR
	NAT         bool           `json:"nat"`
	Nodes       map[string]int `json:"nodes,omitempty"` // Map of node UUID to metric
	Status      bool           `json:"status"`
}

// EgressReq is used for creating and updating egress gateways
type EgressReq struct {
	ID          string         `json:"id,omitempty"` // Required for PUT, omitted for POST
	Name        string         `json:"name"`
	Network     string         `json:"network"`
	Description string         `json:"description,omitempty"`
	Range       string         `json:"range,omitempty"`
	NAT         bool           `json:"nat"`
	Nodes       map[string]int `json:"nodes,omitempty"` // Map of node UUID to metric
	Status      bool           `json:"status"`
}

// EgressCreateResponse wraps the POST /api/v1/egress response
// Code and Message are used for error handling
type EgressCreateResponse struct {
	Code     int    `json:"Code,omitempty"`
	Message  string `json:"Message,omitempty"`
	Response Egress `json:"Response"`
}

// EgressUpdateResponse wraps the PUT /api/v1/egress response
// Code and Message are used for error handling
type EgressUpdateResponse struct {
	Code     int    `json:"Code,omitempty"`
	Message  string `json:"Message,omitempty"`
	Response Egress `json:"Response"`
}
