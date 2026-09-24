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
	Region                      string    `json:"region,omitempty"`
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
	ID         string    `json:"id"`
	GatewayID  string    `json:"gateway_id"`
	AgentID    string    `json:"agent_id"`
	UpstreamID string    `json:"upstream_id,omitempty"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
}

// VMessEndpoint is the persisted external outbound configuration. The name is
// retained for state compatibility; Protocol selects VMess or VLESS Reality.
type VMessEndpoint struct {
	Name        string `json:"name"`
	Protocol    string `json:"protocol,omitempty"`
	Address     string `json:"address"`
	Port        int    `json:"port"`
	UUID        string `json:"uuid"`
	Cipher      string `json:"cipher,omitempty"`
	Network     string `json:"network"`
	Path        string `json:"path,omitempty"`
	Host        string `json:"host,omitempty"`
	TLS         bool   `json:"tls"`
	ServerName  string `json:"server_name,omitempty"`
	Flow        string `json:"flow,omitempty"`
	PublicKey   string `json:"public_key,omitempty"`
	ShortID     string `json:"short_id,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	SpiderX     string `json:"spider_x,omitempty"`
}

type UpstreamCandidate struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type VMessUpstream struct {
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	Enabled         bool                `json:"enabled"`
	SubscriptionURL string              `json:"subscription_url,omitempty"`
	SelectedKey     string              `json:"selected_key,omitempty"`
	Endpoint        VMessEndpoint       `json:"endpoint"`
	Candidates      []UpstreamCandidate `json:"candidates,omitempty"`
	LastRefresh     time.Time           `json:"last_refresh,omitempty"`
	LastError       string              `json:"last_error,omitempty"`
	CreatedAt       time.Time           `json:"created_at"`
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

// RetiredGrant retains only the identity needed to account for the last
// report from a Gateway after an authorization has been deleted.
type RetiredGrant struct {
	UserID        string            `json:"user_id"`
	AttachmentID  string            `json:"attachment_id"`
	GatewayID     string            `json:"gateway_id"`
	LinkNames     map[string]string `json:"link_names"`
	RetireVersion uint64            `json:"retire_version"`
	ExpiresAt     time.Time         `json:"expires_at"`
}

type RetiredLink struct {
	AttachmentID   string    `json:"attachment_id"`
	GatewayID      string    `json:"gateway_id"`
	AgentID        string    `json:"agent_id,omitempty"`
	Name           string    `json:"name"`
	GatewayVersion uint64    `json:"gateway_version"`
	AgentVersion   uint64    `json:"agent_version,omitempty"`
	ExpiresAt      time.Time `json:"expires_at"`
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
	InstanceID        string            `json:"instance_id,omitempty"`
	Online            bool              `json:"online"`
	Ready             bool              `json:"ready"`
	DesiredVersion    uint64            `json:"desired_version"`
	AppliedVersion    uint64            `json:"applied_version"`
	ApplyError        string            `json:"apply_error,omitempty"`
	XrayReady         bool              `json:"xray_ready,omitempty"`
	XrayError         string            `json:"xray_error,omitempty"`
	ExternalUpstreams bool              `json:"external_upstreams,omitempty"`
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

// Updater is the independently authenticated host process. Its secret is never
// included in node configuration or dashboard responses.
type Updater struct {
	NodeID                string    `json:"node_id"`
	Role                  Role      `json:"role"`
	CredentialHash        string    `json:"credential_hash"`
	PendingCredentialHash string    `json:"pending_credential_hash,omitempty"`
	PairTokenHash         string    `json:"pair_token_hash,omitempty"`
	PairExpiresAt         time.Time `json:"pair_expires_at,omitempty"`
	Mode                  string    `json:"mode,omitempty"`
	Arch                  string    `json:"arch,omitempty"`
	Version               string    `json:"version,omitempty"`
	Protocol              int       `json:"protocol,omitempty"`
	LastSeen              time.Time `json:"last_seen,omitempty"`
}

type UpgradeTask struct {
	ID            string    `json:"id"`
	Manual        bool      `json:"manual,omitempty"`
	BatchID       string    `json:"batch_id,omitempty"`
	NodeID        string    `json:"node_id"`
	Role          Role      `json:"role"`
	Actor         string    `json:"actor"`
	FromVersion   string    `json:"from_version"`
	TargetVersion string    `json:"target_version"`
	Asset         string    `json:"asset"`
	BaselineLinks []string  `json:"baseline_links,omitempty"`
	SHA256        string    `json:"sha256"`
	Mode          string    `json:"mode"`
	Arch          string    `json:"arch"`
	Stage         string    `json:"stage"`
	Attempt       int       `json:"attempt"`
	Error         string    `json:"error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type UpgradeBatch struct {
	ID        string    `json:"id"`
	TaskIDs   []string  `json:"task_ids"`
	Stage     string    `json:"stage"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
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
	GrantID         string           `json:"grant_id"`
	ReporterNodeID  string           `json:"reporter_node_id"`
	UploadBytes     uint64           `json:"upload_bytes"`
	DownloadBytes   uint64           `json:"download_bytes"`
	Links           []GrantLinkUsage `json:"links,omitempty"`
	TCPConnections  int              `json:"tcp_connections"`
	UDPAssociations int              `json:"udp_associations"`
	LastError       string           `json:"last_error,omitempty"`
	LastSeen        time.Time        `json:"last_seen"`
}

// GrantLinkUsage counts payload bytes at the Gateway for one grant and Link.
type GrantLinkUsage struct {
	LinkID        string `json:"link_id"`
	UploadBytes   uint64 `json:"upload_bytes"`
	DownloadBytes uint64 `json:"download_bytes"`
}

type UsageBucket struct {
	At            time.Time `json:"at"`
	UploadBytes   uint64    `json:"upload_bytes"`
	DownloadBytes uint64    `json:"download_bytes"`
}

type UsageCounter struct {
	InstanceID    string `json:"instance_id"`
	UploadBytes   uint64 `json:"upload_bytes"`
	DownloadBytes uint64 `json:"download_bytes"`
}

type State struct {
	Revision       uint64                   `json:"revision"`
	Users          map[string]User          `json:"users"`
	Gateways       map[string]Gateway       `json:"gateways"`
	Agents         map[string]Agent         `json:"agents"`
	Upstreams      map[string]VMessUpstream `json:"upstreams"`
	Attachments    map[string]Attachment    `json:"attachments"`
	Links          map[string]Link          `json:"links"`
	Grants         map[string]Grant         `json:"grants"`
	RetiredGrants  map[string]RetiredGrant  `json:"retired_grants,omitempty"`
	RetiredLinks   map[string]RetiredLink   `json:"retired_links,omitempty"`
	Enrollments    map[string]Enrollment    `json:"enrollments"`
	NodeStatus     map[string]NodeStatus    `json:"node_status"`
	LinkStatus     map[string]LinkStatus    `json:"link_status"`
	GrantStatus    map[string]GrantStatus   `json:"grant_status"`
	UsageHistory   map[string][]UsageBucket `json:"usage_history,omitempty"`
	UsageLabels    map[string]string        `json:"usage_labels,omitempty"`
	UsageCounters  map[string]UsageCounter  `json:"usage_counters,omitempty"`
	LinkHistory    map[string][]LinkSample  `json:"link_history,omitempty"`
	Operations     []Operation              `json:"operations,omitempty"`
	Updaters       map[string]Updater       `json:"updaters,omitempty"`
	UpgradeTasks   map[string]UpgradeTask   `json:"upgrade_tasks,omitempty"`
	UpgradeBatches map[string]UpgradeBatch  `json:"upgrade_batches,omitempty"`
}

// LinkSample uses only the Gateway report so tunnel traffic is not counted twice.
type LinkSample struct {
	At            time.Time `json:"at"`
	InstanceID    string    `json:"instance_id,omitempty"`
	Generation    uint64    `json:"generation"`
	UploadBytes   uint64    `json:"upload_bytes"`
	DownloadBytes uint64    `json:"download_bytes"`
	RTTMillis     float64   `json:"rtt_millis"`
	Ready         bool      `json:"ready"`
}

// Operation deliberately excludes request bodies, credentials and result bodies.
type Operation struct {
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Status int       `json:"status"`
}

func NewState() State {
	return State{
		Users:          map[string]User{},
		Gateways:       map[string]Gateway{},
		Agents:         map[string]Agent{},
		Upstreams:      map[string]VMessUpstream{},
		Attachments:    map[string]Attachment{},
		Links:          map[string]Link{},
		Grants:         map[string]Grant{},
		RetiredGrants:  map[string]RetiredGrant{},
		RetiredLinks:   map[string]RetiredLink{},
		Enrollments:    map[string]Enrollment{},
		NodeStatus:     map[string]NodeStatus{},
		LinkStatus:     map[string]LinkStatus{},
		GrantStatus:    map[string]GrantStatus{},
		Updaters:       map[string]Updater{},
		UpgradeTasks:   map[string]UpgradeTask{},
		UpgradeBatches: map[string]UpgradeBatch{},
	}
}
