package model

import "time"

type Role string

const (
	RoleGateway Role = "gateway"
	RoleAgent   Role = "agent"
)

type User struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Enabled           bool      `json:"enabled"`
	SubscriptionToken string    `json:"subscription_token"`
	CreatedAt         time.Time `json:"created_at"`
}

type Gateway struct {
	ID                          string    `json:"id"`
	Name                        string    `json:"name"`
	PublicHost                  string    `json:"public_host"`
	VMessPort                   int       `json:"vmess_port"`
	VMessPath                   string    `json:"vmess_path"`
	VMessHost                   string    `json:"vmess_host"`
	RealityTarget               string    `json:"reality_target,omitempty"`
	RealityName                 string    `json:"reality_name,omitempty"`
	RealityPrivateKey           string    `json:"reality_private_key,omitempty"`
	RealityPublicKey            string    `json:"reality_public_key,omitempty"`
	Enabled                     bool      `json:"enabled"`
	CredentialHash              string    `json:"credential_hash,omitempty"`
	PreviousCredentialHash      string    `json:"previous_credential_hash,omitempty"`
	PreviousCredentialExpiresAt time.Time `json:"previous_credential_expires_at,omitempty"`
	DesiredVersion              uint64    `json:"desired_version"`
	CreatedAt                   time.Time `json:"created_at"`
}

type Agent struct {
	ID                          string    `json:"id"`
	Name                        string    `json:"name"`
	Enabled                     bool      `json:"enabled"`
	AllowedCIDRs                []string  `json:"allowed_cidrs"`
	DeniedCIDRs                 []string  `json:"denied_cidrs,omitempty"`
	AllowedPorts                []int     `json:"allowed_ports,omitempty"`
	CredentialHash              string    `json:"credential_hash,omitempty"`
	PreviousCredentialHash      string    `json:"previous_credential_hash,omitempty"`
	PreviousCredentialExpiresAt time.Time `json:"previous_credential_expires_at,omitempty"`
	DesiredVersion              uint64    `json:"desired_version"`
	CreatedAt                   time.Time `json:"created_at"`
}

type Attachment struct {
	ID        string    `json:"id"`
	GatewayID string    `json:"gateway_id"`
	AgentID   string    `json:"agent_id"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

type Link struct {
	ID              string    `json:"id"`
	AttachmentID    string    `json:"attachment_id"`
	Name            string    `json:"name"`
	URL             string    `json:"url"`
	HTTPHost        string    `json:"http_host,omitempty"`
	TLSServerName   string    `json:"tls_server_name,omitempty"`
	TLSVerify       bool      `json:"tls_verify"`
	RealityUUID     string    `json:"reality_uuid,omitempty"`
	RealityShortID  string    `json:"reality_short_id,omitempty"`
	Priority        int       `json:"priority"`
	Weight          int       `json:"weight"`
	Connections     int       `json:"connections"`
	MaxStreams      int       `json:"max_streams"`
	Enabled         bool      `json:"enabled"`
	TunnelTokenHash string    `json:"tunnel_token_hash,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type Grant struct {
	ID            string    `json:"id"`
	UserID        string    `json:"user_id"`
	AttachmentID  string    `json:"attachment_id"`
	VMessUUID     string    `json:"vmess_uuid"`
	SOCKSUsername string    `json:"socks_username"`
	SOCKSPassword string    `json:"socks_password"`
	Enabled       bool      `json:"enabled"`
	Published     bool      `json:"published"`
	CreatedAt     time.Time `json:"created_at"`
}

type Enrollment struct {
	ID        string    `json:"id"`
	NodeID    string    `json:"node_id"`
	Role      Role      `json:"role"`
	TokenHash string    `json:"token_hash"`
	ExpiresAt time.Time `json:"expires_at"`
	UsedAt    time.Time `json:"used_at,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type NodeStatus struct {
	NodeID            string            `json:"node_id"`
	Role              Role              `json:"role"`
	BinaryVersion     string            `json:"binary_version,omitempty"`
	Online            bool              `json:"online"`
	Ready             bool              `json:"ready"`
	DesiredVersion    uint64            `json:"desired_version"`
	AppliedVersion    uint64            `json:"applied_version"`
	ApplyError        string            `json:"apply_error,omitempty"`
	XrayReady         bool              `json:"xray_ready,omitempty"`
	XrayError         string            `json:"xray_error,omitempty"`
	LastSuccess       time.Time         `json:"last_success,omitempty"`
	LastSeen          time.Time         `json:"last_seen"`
	UploadBytes       uint64            `json:"upload_bytes"`
	DownloadBytes     uint64            `json:"download_bytes"`
	TunnelConnections int               `json:"tunnel_connections"`
	TCPConnections    int               `json:"tcp_connections"`
	UDPAssociations   int               `json:"udp_associations"`
	LastError         string            `json:"last_error,omitempty"`
	FailureCounters   map[string]uint64 `json:"failure_counters,omitempty"`
}

type LinkStatus struct {
	LinkID             string    `json:"link_id"`
	ReporterNodeID     string    `json:"reporter_node_id"`
	Online             bool      `json:"online"`
	Ready              bool      `json:"ready"`
	Generation         uint64    `json:"generation"`
	Connections        int       `json:"connections"`
	ActiveStreams      int       `json:"active_streams"`
	TCPConnections     int       `json:"tcp_connections"`
	UDPAssociations    int       `json:"udp_associations"`
	UploadBytes        uint64    `json:"upload_bytes"`
	DownloadBytes      uint64    `json:"download_bytes"`
	RTTMillis          float64   `json:"rtt_millis"`
	LastSuccess        time.Time `json:"last_success,omitempty"`
	LastError          string    `json:"last_error,omitempty"`
	ProbeTimeouts      uint64    `json:"probe_timeouts"`
	QueueDrops         uint64    `json:"queue_drops"`
	WriteBlockedMillis uint64    `json:"write_blocked_millis,omitempty"`
	WriteStalls        uint64    `json:"write_stalls,omitempty"`
	LastSeen           time.Time `json:"last_seen"`
}

type GrantStatus struct {
	GrantID         string    `json:"grant_id"`
	ReporterNodeID  string    `json:"reporter_node_id"`
	UploadBytes     uint64    `json:"upload_bytes"`
	DownloadBytes   uint64    `json:"download_bytes"`
	TCPConnections  int       `json:"tcp_connections"`
	UDPAssociations int       `json:"udp_associations"`
	LastError       string    `json:"last_error,omitempty"`
	LastSeen        time.Time `json:"last_seen"`
}

type State struct {
	Revision    uint64                 `json:"revision"`
	Users       map[string]User        `json:"users"`
	Gateways    map[string]Gateway     `json:"gateways"`
	Agents      map[string]Agent       `json:"agents"`
	Attachments map[string]Attachment  `json:"attachments"`
	Links       map[string]Link        `json:"links"`
	Grants      map[string]Grant       `json:"grants"`
	Enrollments map[string]Enrollment  `json:"enrollments"`
	NodeStatus  map[string]NodeStatus  `json:"node_status"`
	LinkStatus  map[string]LinkStatus  `json:"link_status"`
	GrantStatus map[string]GrantStatus `json:"grant_status"`
}

func NewState() State {
	return State{
		Users:       map[string]User{},
		Gateways:    map[string]Gateway{},
		Agents:      map[string]Agent{},
		Attachments: map[string]Attachment{},
		Links:       map[string]Link{},
		Grants:      map[string]Grant{},
		Enrollments: map[string]Enrollment{},
		NodeStatus:  map[string]NodeStatus{},
		LinkStatus:  map[string]LinkStatus{},
		GrantStatus: map[string]GrantStatus{},
	}
}
