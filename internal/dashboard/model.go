package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Fault struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
}

func (e *Fault) Error() string         { return e.Message }
func bad(message string) error         { return &Fault{"validation", message, 400} }
func conflict(message string) error    { return &Fault{"conflict", message, 409} }
func unavailable(message string) error { return &Fault{"providerUnavailable", message, 502} }

// Secret pointers exist only in write DTOs. Omitted preserves; empty explicitly clears.
type SSHConfig struct {
	Enabled        bool   `json:"enabled"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	User           string `json:"user"`
	PrivateKeyFile string `json:"privateKeyFile"`
	KnownHostsFile string `json:"knownHostsFile"`
}
type Host struct {
	AddressProfiles []AddressProfile `json:"addressProfiles,omitempty"`
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	BaseURL         string           `json:"baseUrl"`
	Node            string           `json:"node"`
	OPNsenseID      string           `json:"opnsenseId"`
	SubnetID        string           `json:"subnetId,omitempty"`
	VMIDMin         int              `json:"vmidMin"`
	VMIDMax         int              `json:"vmidMax"`
	Network         string           `json:"network"`
	MACPrefix       string           `json:"macPrefix"`
	TokenID         string           `json:"tokenId"`
	TokenSecret     string           `json:"tokenSecret,omitempty"`
	CAPEM           string           `json:"caPem,omitempty"`
	SSH             SSHConfig        `json:"ssh"`
}
type AddressProfile struct {
	VMIDMin   int    `json:"vmidMin"`
	VMIDMax   int    `json:"vmidMax"`
	Network   string `json:"network"`
	MACPrefix string `json:"macPrefix"`
}
type OPNsense struct {
	PrivateAccessListID string   `json:"privateAccessListId"`
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	BaseURL             string   `json:"baseUrl"`
	APIKey              string   `json:"apiKey,omitempty"`
	APISecret           string   `json:"apiSecret,omitempty"`
	CAPEM               string   `json:"caPem,omitempty"`
	SubnetID            string   `json:"subnetId"`
	DomainBase          string   `json:"domainBase"`
	AllowlistCIDRs      []string `json:"allowlistCidrs"`
	UpstreamScheme      string   `json:"upstreamScheme"`
	UpstreamPort        int      `json:"upstreamPort"`
	CertificateID       string   `json:"certificateId"`
	DomainReady         bool     `json:"domainReady"`
}
type Config struct {
	Revision  int        `json:"revision"`
	Hosts     []Host     `json:"hosts"`
	OPNsenses []OPNsense `json:"opnsenses"`
}
type HostEdit struct {
	Host
	Secret *string `json:"tokenSecret"`
}
type OPNsenseEdit struct {
	OPNsense
	Secret      *string `json:"apiSecret"`
	APIKeyValue *string `json:"apiKey"`
}
type ConfigEdit struct {
	Revision  int            `json:"revision"`
	Hosts     []HostEdit     `json:"hosts"`
	OPNsenses []OPNsenseEdit `json:"opnsenses"`
}
type HostView struct {
	Host
	HasTokenSecret bool `json:"hasTokenSecret"`
}
type OPNsenseView struct {
	OPNsense
	HasAPISecret bool `json:"hasApiSecret"`
	HasAPIKey    bool `json:"hasApiKey"`
}
type ConfigView struct {
	Revision  int            `json:"revision"`
	Hosts     []HostView     `json:"hosts"`
	OPNsenses []OPNsenseView `json:"opnsenses"`
}

func viewConfig(c Config) ConfigView {
	v := ConfigView{c.Revision, []HostView{}, []OPNsenseView{}}
	for _, h := range c.Hosts {
		yes := h.TokenSecret != ""
		h.TokenSecret = ""
		v.Hosts = append(v.Hosts, HostView{h, yes})
	}
	for _, o := range c.OPNsenses {
		yes := o.APISecret != ""
		keySet := o.APIKey != ""
		o.APISecret = ""
		o.APIKey = ""
		v.OPNsenses = append(v.OPNsenses, OPNsenseView{o, yes, keySet})
	}
	return v
}
func (c Config) Host(id string) (Host, bool) {
	for _, h := range c.Hosts {
		if h.ID == id {
			return h, true
		}
	}
	return Host{}, false
}
func routerForHost(c Config, h Host) OPNsense {
	o, _ := c.Router(h.OPNsenseID)
	if h.SubnetID != "" {
		o.SubnetID = h.SubnetID
	}
	return o
}

func (c Config) Router(id string) (OPNsense, bool) {
	for _, o := range c.OPNsenses {
		if o.ID == id {
			return o, true
		}
	}
	return OPNsense{}, false
}

var slug = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)
var hostname = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var generationMarker = regexp.MustCompile(`(?:^|[\s])container-dashboard-generation=([a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12})(?:$|[\s])`)
var macPrefixPattern = regexp.MustCompile(`(?i)^(?:[0-9a-f]{2}:){4}[0-9a-f]{2}$`)
var uuidPattern = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

func validURL(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/")
}
func validDomain(s string) bool {
	if len(s) > 240 || !strings.Contains(s, ".") {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if !hostname.MatchString(part) {
			return false
		}
	}
	return true
}
func ValidateConfig(c Config) error {
	seen := map[string]bool{}
	nodes := map[string]bool{}
	for _, o := range c.OPNsenses {
		if !slug.MatchString(o.ID) || seen[o.ID] || !validURL(o.BaseURL) || o.Name == "" {
			return bad("invalid or duplicate OPNsense configuration")
		}
		seen[o.ID] = true
		if o.PrivateAccessListID != "" && !uuidPattern.MatchString(o.PrivateAccessListID) {
			return bad("invalid private access list UUID")
		}
		if (o.SubnetID != "" && !uuidPattern.MatchString(o.SubnetID)) || !validDomain(o.DomainBase) {
			return bad("invalid OPNsense subnet, domain or upstream")
		}
		for _, s := range o.AllowlistCIDRs {
			p, e := netip.ParsePrefix(s)
			if e != nil || p.Bits() == 0 || p.Masked() != p {
				return bad("allowlist must contain explicit restricted canonical CIDRs")
			}
		}
		if unrestrictedCIDRs(o.AllowlistCIDRs) {
			return bad("combined allowlist CIDRs must not allow an entire address family")
		}
		if len(o.AllowlistCIDRs) > 64 || len(o.CAPEM) > 128*1024 || len(o.APISecret) > 8192 || len(o.APIKey) > 8192 {
			return bad("OPNsense configuration exceeds limits")
		}
	}
	seen = map[string]bool{}
	for _, h := range c.Hosts {
		if !slug.MatchString(h.ID) || seen[h.ID] || !slug.MatchString(h.Node) || !validURL(h.BaseURL) || h.Name == "" {
			return bad("invalid or duplicate Proxmox host")
		}
		seen[h.ID] = true
		if h.SubnetID != "" && !uuidPattern.MatchString(h.SubnetID) {
			return bad("invalid host subnet UUID")
		}
		if _, ok := c.Router(h.OPNsenseID); !ok {
			return bad("host references an unknown OPNsense")
		}
		nodeKey := strings.TrimRight(h.BaseURL, "/") + "/" + h.Node
		if nodes[nodeKey] {
			return bad("duplicate Proxmox endpoint and node")
		}
		nodes[nodeKey] = true
		if h.VMIDMin < 100 || h.VMIDMax < h.VMIDMin || h.VMIDMax > 9999 || h.VMIDMin/100 != h.VMIDMax/100 || h.VMIDMin%100 == 0 {
			return bad("VMID bounds must be in one hundred block with suffix 01..99")
		}
		if _, _, e := DeriveAddress(h, h.VMIDMin); e != nil {
			return e
		}
		if _, _, e := DeriveAddress(h, h.VMIDMax); e != nil {
			return e
		}
		if len(h.AddressProfiles) > 16 {
			return bad("too many address profiles")
		}
		ranges := []AddressProfile{{h.VMIDMin, h.VMIDMax, h.Network, h.MACPrefix}}
		for _, profile := range h.AddressProfiles {
			if profile.VMIDMin < 100 || profile.VMIDMax < profile.VMIDMin || profile.VMIDMax > 9999 || profile.VMIDMin/100 != profile.VMIDMax/100 || profile.VMIDMin%100 == 0 {
				return bad("invalid additional address profile bounds")
			}
			for _, prior := range ranges {
				if profile.VMIDMin <= prior.VMIDMax && profile.VMIDMax >= prior.VMIDMin {
					return bad("address profile VMID ranges overlap")
				}
			}
			candidate := h
			candidate.VMIDMin, candidate.VMIDMax, candidate.Network, candidate.MACPrefix = profile.VMIDMin, profile.VMIDMax, profile.Network, profile.MACPrefix
			if _, _, err := deriveProfileAddress(candidate, profile.VMIDMin); err != nil {
				return err
			}
			if _, _, err := deriveProfileAddress(candidate, profile.VMIDMax); err != nil {
				return err
			}
			ranges = append(ranges, profile)
		}
		if len(h.TokenSecret) > 8192 || len(h.CAPEM) > 128*1024 || strings.ContainsAny(h.TokenID+h.TokenSecret, "\r\n") {
			return bad("invalid token configuration")
		}
		if h.SSH.Enabled {
			if e := validateSSH(h.SSH); e != nil {
				return e
			}
		}
	}
	if len(c.Hosts) > 100 || len(c.OPNsenses) > 50 {
		return bad("too many providers")
	}
	return nil
}
func validateSSH(s SSHConfig) error {
	if !slug.MatchString(s.User) || s.Port < 1 || s.Port > 65535 || s.Host == "" || strings.HasPrefix(s.Host, "-") || s.PrivateKeyFile == "" || s.KnownHostsFile == "" {
		return bad("SSH requires host, port, user, private key and known-hosts file")
	}
	if net.ParseIP(s.Host) == nil {
		for _, p := range strings.Split(s.Host, ".") {
			if !hostname.MatchString(strings.ToLower(p)) {
				return bad("invalid SSH host")
			}
		}
	}
	return nil
}
func DeriveAddress(h Host, id int) (string, string, error) {
	found := id >= h.VMIDMin && id <= h.VMIDMax
	selected := h
	for _, profile := range h.AddressProfiles {
		if id < profile.VMIDMin || id > profile.VMIDMax {
			continue
		}
		if found {
			return "", "", conflict("multiple address profiles match the container VMID")
		}
		selected.VMIDMin, selected.VMIDMax, selected.Network, selected.MACPrefix = profile.VMIDMin, profile.VMIDMax, profile.Network, profile.MACPrefix
		found = true
	}
	if !found {
		return "", "", bad("VMID is outside the host address profiles")
	}
	return deriveProfileAddress(selected, id)
}
func deriveProfileAddress(h Host, id int) (string, string, error) {
	p, e := netip.ParsePrefix(h.Network)
	if e != nil || !p.Addr().Is4() || p.Bits() != 24 || p != p.Masked() {
		return "", "", bad("host network must be a canonical IPv4 /24")
	}
	if id < h.VMIDMin || id > h.VMIDMax || id%100 == 0 {
		return "", "", bad("VMID is outside the host profile")
	}
	oct := p.Addr().As4()
	hi := id / 100
	if hi > 99 || int(oct[2]) != hi {
		return "", "", bad("VMID range and network prefix mismatch")
	}
	want := fmt.Sprintf("%02d", hi)
	if !macPrefixPattern.MatchString(h.MACPrefix) {
		return "", "", bad("MAC prefix must have five two-digit bytes")
	}
	mac, e := net.ParseMAC(strings.ToLower(h.MACPrefix) + ":00")
	if e != nil || len(mac) != 6 || !strings.EqualFold(strings.Split(h.MACPrefix, ":")[4], want) {
		return "", "", bad("MAC prefix and VMID range mismatch")
	}
	oct[3] = byte(id % 100)
	return strings.ToLower(h.MACPrefix) + fmt.Sprintf(":%02d", id%100), netip.AddrFrom4(oct).String(), nil
}
func DefaultProfiles() []Host {
	return []Host{{ID: "main", Name: "Main", VMIDMin: 601, VMIDMax: 699, Network: "192.168.6.0/24", MACPrefix: "d0:cc:37:00:06"}, {ID: "pvedell", Name: "pvedell", VMIDMin: 701, VMIDMax: 799, Network: "192.168.7.0/24", MACPrefix: "d0:cc:37:00:07"}}
}
func fingerprint(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func key(host string, id int) string { return host + ":" + strconv.Itoa(id) }

type RouteConfig struct {
	Scheme string `json:"scheme"`
	Port   int    `json:"port"`
	Public bool   `json:"public"`
}
type FieldChange struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}
type PlanEffect struct {
	Kind       string        `json:"kind"`
	Action     string        `json:"action"`
	ResourceID string        `json:"resourceId"`
	Name       string        `json:"name"`
	Changes    []FieldChange `json:"changes"`
}

type Container struct {
	ExpectedIP   string       `json:"expectedIp,omitempty"`
	ExpectedMAC  string       `json:"expectedMac,omitempty"`
	DesiredRoute *RouteConfig `json:"desiredRoute,omitempty"`
	Key          string       `json:"key"`
	HostID       string       `json:"hostId"`
	Node         string       `json:"node"`
	VMID         int          `json:"vmid"`
	Name         string       `json:"name"`
	Status       string       `json:"status"`
	Template     bool         `json:"template"`
	IP           string       `json:"ip"`
	MAC          string       `json:"mac"`
	Distribution string       `json:"distribution"`
	Version      string       `json:"version"`
	DistroSource string       `json:"distroSource"`
	App          string       `json:"app"`
	Icon         string       `json:"icon"`
	Identity     string       `json:"identity"`
	Fingerprint  string       `json:"fingerprint"`
	Warnings     []string     `json:"warnings"`
}
type Guest struct {
	VMID int    `json:"vmid"`
	Type string `json:"type"`
	Node string `json:"node"`
}
type HostInventory struct {
	HostID   string  `json:"hostId"`
	Complete bool    `json:"complete"`
	Error    string  `json:"error"`
	NextVMID *int    `json:"nextVmid"`
	Guests   []Guest `json:"guests"`
}
type Inventory struct {
	ObservedAt time.Time       `json:"observedAt"`
	Complete   bool            `json:"complete"`
	Containers []Container     `json:"containers"`
	Hosts      []HostInventory `json:"hosts"`
}

func (i Inventory) Find(k string) (Container, bool) {
	for _, c := range i.Containers {
		if c.Key == k {
			return c, true
		}
	}
	return Container{}, false
}
func (i Inventory) StableHash() string {
	return fingerprint(struct {
		Containers []Container
		Hosts      []HostInventory
	}{i.Containers, i.Hosts})
}
func NextVMID(h Host, guests []Guest, complete bool) (*int, error) {
	if !complete {
		return nil, unavailable("cluster inventory is incomplete")
	}
	occupied := map[int]bool{}
	for _, g := range guests {
		occupied[g.VMID] = true
	}
	candidates := []int{}
	ranges := append([]AddressProfile{{h.VMIDMin, h.VMIDMax, h.Network, h.MACPrefix}}, h.AddressProfiles...)
	for _, profile := range ranges {
		for id := profile.VMIDMin; id <= profile.VMIDMax; id++ {
			candidates = append(candidates, id)
		}
	}
	sort.Ints(candidates)
	for _, id := range candidates {
		if !occupied[id] {
			return &id, nil
		}
	}
	return nil, nil
}
func inferApp(name string) (string, string) {
	for _, a := range []string{"paperless", "immich", "vaultwarden", "homebridge", "uptime-kuma", "adguard", "jellyfin", "plex", "grafana", "gitea", "forgejo", "homeassistant", "nginx", "postgres", "redis", "actualbudget"} {
		if strings.Contains(strings.ToLower(name), a) {
			return a, a
		}
	}
	return "unknown", "container"
}

type Resource struct {
	ID     string            `json:"id"`
	Kind   string            `json:"kind"`
	Fields map[string]string `json:"fields"`
}

func (r Resource) Fingerprint() string { return fingerprint(r) }

type ProviderSnapshot struct {
	ClientIPContext *ClientIPContext `json:"clientIpContext,omitempty"`
	Resources       []Resource       `json:"resources"`
	Subnet          string           `json:"subnet"`
	Ready           bool             `json:"ready"`
	Issues          []string         `json:"issues"`
}

func (p ProviderSnapshot) Find(id, kind string) (Resource, bool) {
	for _, r := range p.Resources {
		if r.ID == id && r.Kind == kind {
			return r, true
		}
	}
	return Resource{}, false
}

type OwnedResource struct {
	SubnetID    string `json:"subnetId,omitempty"`
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
}
type Ownership struct {
	ResolvedSubnet   *SubnetReference     `json:"resolvedSubnet,omitempty"`
	SharedAccessList *AccessListReference `json:"sharedAccessList,omitempty"`
	DesiredRoute     RouteConfig          `json:"desiredRoute"`
	Key              string               `json:"key"`
	HostID           string               `json:"hostId"`
	VMID             int                  `json:"vmid"`
	OPNsenseID       string               `json:"opnsenseId"`
	Identity         string               `json:"identity"`
	Public           bool                 `json:"public"`
	Resources        []OwnedResource      `json:"resources"`
}
type Plan struct {
	ResolvedSubnet   *SubnetReference     `json:"resolvedSubnet,omitempty"`
	SharedAccessList *AccessListReference `json:"sharedAccessList,omitempty"`
	DesiredRoute     RouteConfig          `json:"desiredRoute"`
	Effects          []PlanEffect         `json:"effects"`
	ApplyNotes       []string             `json:"applyNotes"`
	ID               string               `json:"id"`
	Action           string               `json:"action"`
	Key              string               `json:"key"`
	HostID           string               `json:"hostId"`
	VMID             int                  `json:"vmid"`
	OPNsenseID       string               `json:"opnsenseId"`
	Public           bool                 `json:"public"`
	Revision         int                  `json:"revision"`
	InventoryHash    string               `json:"inventoryHash"`
	ProviderHash     string               `json:"providerHash"`
	Identity         string               `json:"identity"`
	CreatedAt        time.Time            `json:"createdAt"`
	ExpiresAt        time.Time            `json:"expiresAt"`
}
type PlanRequest struct {
	UpstreamScheme *string `json:"upstreamScheme,omitempty"`
	UpstreamPort   *int    `json:"upstreamPort,omitempty"`
	Action         string  `json:"action"`
	HostID         string  `json:"hostId"`
	VMID           int     `json:"vmid"`
	Public         bool    `json:"public"`
}
type Operation struct {
	DesiredRoute         *RouteConfig         `json:"desiredRoute,omitempty"`
	ResolvedSubnet       *SubnetReference     `json:"resolvedSubnet,omitempty"`
	SharedAccessList     *AccessListReference `json:"sharedAccessList,omitempty"`
	ContainerFingerprint string               `json:"containerFingerprint,omitempty"`
	ID                   string               `json:"id"`
	PlanID               string               `json:"planId"`
	Key                  string               `json:"key"`
	OPNsenseID           string               `json:"opnsenseId"`
	Action               string               `json:"action"`
	Status               string               `json:"status"`
	Step                 string               `json:"step"`
	Error                string               `json:"error"`
	ManualRecovery       bool                 `json:"manualRecovery"`
	CreatedAt            time.Time            `json:"createdAt"`
	UpdatedAt            time.Time            `json:"updatedAt"`
	Resources            []OwnedResource      `json:"resources"`
}
type Job struct {
	ID           string    `json:"id"`
	Key          string    `json:"key"`
	HostID       string    `json:"hostId"`
	VMID         int       `json:"vmid"`
	Kind         string    `json:"kind"`
	Fingerprint  string    `json:"fingerprint"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	Error        string    `json:"error"`
	Acknowledged bool      `json:"acknowledged"`
	Logs         []string  `json:"logs"`
	Truncated    bool      `json:"truncated"`
}
