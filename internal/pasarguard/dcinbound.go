// dcinbound.go — the per-datacenter REALITY inbound reconciler (phase 14
// task 2; design 00-ADMIN/PASARGUARD-PER-DC-DESIGN.md). Model change vs
// the node reconciler in reconcile.go: one NODE per foreign server (the
// legacy reconciler keeps managing those), and ONE VLESS+Reality INBOUND
// per (foreign server × iran datacenter) inside the core config the node
// references, plus ONE PasarGuard HOST per pair mapping what clients see
// (address = the iran server, port = 443, sni = the DC's SNI) onto the
// inbound tag.
//
// Ownership contract: inbound tags / host remarks Quantum owns are
// "quantum:<dcslug>:<serverslug>". Counterparts are matched by the FULL
// canonical name (exact, then itself as a prefix for suffix junk) — never
// by the dc part alone, because upstream cores can be SHARED by several
// servers' nodes (the legacy reconciler puts every node on core 1) and
// hosts are global records. The diff NEVER plans changes to inbounds
// outside the "quantum:" prefix and never deletes anything: superseded
// pairs are reported stale and an explicit operator action removes them.
//
// The core is where inbounds live upstream (GET/PUT /api/core/{id}; nodes
// reference it via core_config_id). restart_nodes=true on the PUT makes
// every node on the core re-fetch the config, so no separate reconnect
// round is needed in the happy path.

package pasarguard

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// InboundPlan is one VLESS+Reality inbound desired on a foreign server's
// core. PrivateKey is xray's realitySettings.privateKey (base64 RawURL);
// it is sealed at rest by the panel and NEVER serialized (json:"-"): it
// must not reach a log, an audit line or an API response.
type InboundPlan struct {
	Tag        string   `json:"tag"`
	Listen     string   `json:"listen"` // always 127.0.0.1: the tunnel delivers the traffic
	Port       int      `json:"port"`
	SNIs       []string `json:"sni,omitempty"`
	Dest       string   `json:"dest,omitempty"`
	ShortIDs   []string `json:"short_ids,omitempty"`
	PrivateKey string   `json:"-"`
	PublicKey  string   `json:"public_key,omitempty"` // not secret; goes into client configs
	// ClientAddress is the iran server address clients connect through
	// (drives the host record's address; NOT part of the xray inbound).
	ClientAddress string `json:"client_address,omitempty"`
}

// HostPlan is one client-facing host mapping: what clients connect with
// (address/port/SNI) onto which inbound tag.
type HostPlan struct {
	Remark     string `json:"remark"`
	Address    string `json:"address"`
	Port       int    `json:"port"`
	SNI        string `json:"sni,omitempty"`
	InboundTag string `json:"inbound_tag"`
}

// DCInboundPlan is the diff result for ONE foreign server's core (JSON
// shape of the dry-run response). Stale lists quantum-owned inbound tags
// the desired set no longer covers — reported, never deleted.
type DCInboundPlan struct {
	Server      string        `json:"server"`
	CoreID      int           `json:"core_id"`
	Create      []InboundPlan `json:"create"`
	Modify      []InboundPlan `json:"modify"`
	HostsCreate []HostPlan    `json:"hosts_create"`
	HostsModify []HostPlan    `json:"hosts_modify"`
	Stale       []string      `json:"stale"`
}

// Drift reports whether the plan would change anything upstream.
func (p *DCInboundPlan) Drift() bool {
	if p == nil {
		return false
	}
	return len(p.Create) > 0 || len(p.Modify) > 0 ||
		len(p.HostsCreate) > 0 || len(p.HostsModify) > 0 || len(p.Stale) > 0
}

// DCInboundTag builds the canonical tag AND host remark Quantum owns for
// one (datacenter, foreign server) pair: "quantum:<dc>:<server>". Tags
// and remarks share the shape so a host maps 1:1 onto its inbound.
func DCInboundTag(dcSlug, serverSlug string) string {
	return NamePrefix + slugPart(dcSlug) + ":" + slugPart(serverSlug)
}

// CoreModifyRequest mirrors upstream CoreCreate — the body of
// PUT /api/core/{id}?restart_nodes= (app/models/core.py CoreCreate; the
// Xray JSON lives in Config). The applier round-trips the GET /api/core
// record through this so untouched fields survive.
type CoreModifyRequest struct {
	Name                 string         `json:"name"`
	Config               map[string]any `json:"config"`
	Type                 string         `json:"type,omitempty"`
	ExcludeInboundTags   []string       `json:"exclude_inbound_tags,omitempty"`
	FallbacksInboundTags []string       `json:"fallbacks_inbound_tags,omitempty"`
}

// Host is the subset of upstream BaseHost (app/models/host.py) the
// reconciler reads from GET /api/hosts (a bare JSON array).
type Host struct {
	ID         int      `json:"id"`
	Remark     string   `json:"remark"`
	Address    []string `json:"address,omitempty"`
	InboundTag string   `json:"inbound_tag,omitempty"`
	Port       int      `json:"port,omitempty"`
	SNI        []string `json:"sni,omitempty"`
	Security   string   `json:"security,omitempty"`
}

// HostCreateRequest mirrors upstream CreateHost. The modify endpoint
// takes the SAME full document (host.py modify_host uses CreateHost), so
// one type serves POST /api/host/ and PUT /api/host/{id}. Priority is
// REQUIRED upstream (BaseHost, no default — a missing field is a 422;
// verified live) and is always sent: 0 sorts Quantum's hosts first.
type HostCreateRequest struct {
	Remark     string   `json:"remark"`
	Address    []string `json:"address,omitempty"`
	InboundTag string   `json:"inbound_tag,omitempty"`
	Port       int      `json:"port,omitempty"`
	SNI        []string `json:"sni,omitempty"`
	Host       []string `json:"host,omitempty"`
	Security   string   `json:"security,omitempty"`
	Priority   int      `json:"priority"`
}

// SlugPart is the exported form of slugPart: lowercase [a-z0-9-_], ≤ 24
// chars — the safe building block for tags, remarks and ledger owner keys.
func SlugPart(s string) string { return slugPart(s) }

// ---- reality keys -----------------------------------------------------------

// GenerateRealityKeyPair creates one REALITY x25519 key pair with Xray's
// exact semantics: 32 random bytes, RFC 7748-clamped, base64.RawURLEncoding.
// The public key derives from the private (upstream get_x25519_public_key
// applies the same RFC 7748 derivation), so only the private key needs
// storing and it never travels anywhere but the sealed store.
func GenerateRealityKeyPair() (privateB64, publicB64 string, err error) {
	var pvk [32]byte
	if _, err = rand.Read(pvk[:]); err != nil {
		return "", "", err
	}
	pvk[0] &= 248
	pvk[31] &= 127
	pvk[31] |= 64
	priv, err := ecdh.X25519().NewPrivateKey(pvk[:])
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(pvk[:]),
		base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}

// RealityPublicKey derives the public key for a private key (both base64
// RawURL) — the same derivation the upstream panel applies to every
// inbound's privateKey.
func RealityPublicKey(privateB64 string) (string, error) {
	raw, err := realityKeyBytes(privateB64)
	if err != nil {
		return "", err
	}
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", fmt.Errorf("pasarguard: bad reality private key: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}

// RealityShortID derives a deterministic 8-hex-char shortId from the
// private key bytes: stable per key pair (a re-apply never flips the
// client-facing shortId), no extra secret material to store.
func RealityShortID(privateB64 string) (string, error) {
	raw, err := realityKeyBytes(privateB64)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:4]), nil
}

func realityKeyBytes(privateB64 string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(privateB64))
	if err != nil || len(raw) != 32 {
		return nil, errors.New("pasarguard: reality private key must be 32 raw bytes, base64url")
	}
	return raw, nil
}

// ---- diff -------------------------------------------------------------------

// DiffDCInbounds computes the per-DC plan for one core's config. Pure
// function: no client calls, fully testable.
//
//	config        the core record's "config" map (its "inbounds" array is the state)
//	desired       the inbounds Quantum wants on this core
//	existingHosts the panel's current host list (matched by remark)
//	ourTagPrefix  the ownership prefix ("quantum:"; "" uses NamePrefix)
//
// Matching: a desired row claims its counterpart by EXACT tag/remark, then
// by its own full name as a PREFIX (suffix junk on our own name). Matching
// only on the "quantum:<dc>:" part is deliberately NOT done: cores can be
// SHARED by several servers' nodes (the legacy reconciler provisions every
// node on core 1) and hosts are global upstream, so a dc-only prefix would
// grab another server's records. A renamed server slot therefore shows up
// as create(new) + stale(old inbound) — visible, never auto-deleted.
func DiffDCInbounds(config map[string]any, desired []InboundPlan, existingHosts []Host, ourTagPrefix string) *DCInboundPlan {
	if ourTagPrefix == "" {
		ourTagPrefix = NamePrefix
	}
	plan := &DCInboundPlan{
		Create:      []InboundPlan{},
		Modify:      []InboundPlan{},
		HostsCreate: []HostPlan{},
		HostsModify: []HostPlan{},
		Stale:       []string{},
	}

	inbounds := extractInbounds(config)
	claimed := make([]bool, len(inbounds))
	for _, want := range desired {
		idx := matchInbound(inbounds, claimed, want.Tag)
		if idx < 0 {
			plan.Create = append(plan.Create, want)
			continue
		}
		claimed[idx] = true
		if !inboundMatches(inbounds[idx], want) {
			plan.Modify = append(plan.Modify, want)
		}
	}
	// Unclaimed quantum-owned inbounds are stale (core-scoped, so this can
	// only name THIS core's inbounds); anything else on the core is the
	// operator's and invisible to this diff.
	for i, in := range inbounds {
		if claimed[i] {
			continue
		}
		if tag, _ := in["tag"].(string); strings.HasPrefix(tag, ourTagPrefix) {
			plan.Stale = append(plan.Stale, tag)
		}
	}

	// Hosts: same counterpart rule on the remark. Host records are GLOBAL
	// upstream, so orphans (removed pairs, renames) belong to no single
	// server's diff — they are never reported here and never deleted; an
	// explicit operator action cleans them.
	claimedHost := make([]bool, len(existingHosts))
	for _, want := range desiredHostPlans(desired) {
		idx := matchHost(existingHosts, claimedHost, want.Remark)
		if idx < 0 {
			plan.HostsCreate = append(plan.HostsCreate, want)
			continue
		}
		claimedHost[idx] = true
		if !hostMatches(existingHosts[idx], want) {
			plan.HostsModify = append(plan.HostsModify, want)
		}
	}

	sort.SliceStable(plan.Create, func(i, j int) bool { return plan.Create[i].Tag < plan.Create[j].Tag })
	sort.SliceStable(plan.Modify, func(i, j int) bool { return plan.Modify[i].Tag < plan.Modify[j].Tag })
	sort.SliceStable(plan.HostsCreate, func(i, j int) bool { return plan.HostsCreate[i].Remark < plan.HostsCreate[j].Remark })
	sort.SliceStable(plan.HostsModify, func(i, j int) bool { return plan.HostsModify[i].Remark < plan.HostsModify[j].Remark })
	sort.Strings(plan.Stale)
	return plan
}

// desiredHostPlans derives the host plans from the inbound plans: one
// host per inbound (address = the iran server's address, port 443, sni =
// the DC's SNI, remark = the inbound tag).
func desiredHostPlans(desired []InboundPlan) []HostPlan {
	out := make([]HostPlan, 0, len(desired))
	for _, in := range desired {
		sni := ""
		if len(in.SNIs) > 0 {
			sni = in.SNIs[0]
		}
		out = append(out, HostPlan{
			Remark:     in.Tag,
			Address:    in.ClientAddress,
			Port:       443,
			SNI:        sni,
			InboundTag: in.Tag,
		})
	}
	return out
}

// matchInbound finds the unclaimed counterpart of one desired inbound:
// exact tag first, then the desired tag itself as a prefix (tolerates
// suffix variants on our own name only).
func matchInbound(inbounds []map[string]any, claimed []bool, tag string) int {
	for i, in := range inbounds {
		if claimed[i] {
			continue
		}
		if t, _ := in["tag"].(string); t == tag {
			return i
		}
	}
	for i, in := range inbounds {
		if claimed[i] {
			continue
		}
		if t, _ := in["tag"].(string); t != tag && strings.HasPrefix(t, tag) {
			return i
		}
	}
	return -1
}

// matchHost is matchInbound on the remark (hosts are global upstream, so
// the counterpart scope is the FULL desired remark, never a dc prefix).
func matchHost(hosts []Host, claimed []bool, remark string) int {
	for i, h := range hosts {
		if claimed[i] {
			continue
		}
		if h.Remark == remark {
			return i
		}
	}
	for i, h := range hosts {
		if claimed[i] {
			continue
		}
		if h.Remark != remark && strings.HasPrefix(h.Remark, remark) {
			return i
		}
	}
	return -1
}

// numericInt coerces the port values the diff can see: float64 from a
// JSON-decoded config, int/int64 from an in-memory one.
func numericInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}

// inboundMatches compares the xray inbound JSON Quantum manages against
// the desired plan. Anything unreadable counts as drift (the modify
// rewrites the whole document). The tag itself is NOT compared: a
// prefix-matched counterpart may carry an older server suffix and the
// modify replaces it.
func inboundMatches(existing map[string]any, want InboundPlan) bool {
	if listen, _ := existing["listen"].(string); listen != want.Listen {
		return false
	}
	if port, ok := numericInt(existing["port"]); !ok || port != want.Port {
		return false
	}
	if proto, _ := existing["protocol"].(string); proto != "vless" {
		return false
	}
	stream, _ := existing["streamSettings"].(map[string]any)
	if stream == nil {
		return false
	}
	if sec, _ := stream["security"].(string); sec != "reality" {
		return false
	}
	reality, _ := stream["realitySettings"].(map[string]any)
	if reality == nil {
		return false
	}
	if dest, _ := reality["dest"].(string); dest != want.Dest {
		return false
	}
	if pvk, _ := reality["privateKey"].(string); pvk != want.PrivateKey {
		return false
	}
	if snis, ok := stringSlice(reality["serverNames"]); !ok || !strSetEqual(snis, want.SNIs) {
		return false
	}
	if sids, ok := stringSlice(reality["shortIds"]); !ok || !strSetEqual(sids, want.ShortIDs) {
		return false
	}
	return true
}

// stringSlice coerces the string arrays the diff can see: []any from a
// JSON-decoded config, []string from an in-memory one.
func stringSlice(v any) ([]string, bool) {
	switch s := v.(type) {
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			str, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, str)
		}
		return out, true
	case []string:
		return s, true
	}
	return nil, false
}

// hostMatches compares one existing host record against the desired plan.
func hostMatches(existing Host, want HostPlan) bool {
	if existing.Port != want.Port || existing.InboundTag != want.InboundTag {
		return false
	}
	if !strListContains(existing.Address, want.Address) {
		return false
	}
	if !strListContains(existing.SNI, want.SNI) {
		return false
	}
	return true
}

func strListContains(list []string, want string) bool {
	if want == "" {
		return true
	}
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// strSetEqual compares two string sets (order-insensitive; duplicates
// tolerated on both sides).
func strSetEqual(got, want []string) bool {
	left := append([]string{}, got...)
	right := append([]string{}, want...)
	sort.Strings(left)
	sort.Strings(right)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// extractInbounds reads config["inbounds"] as a list of maps (nil-safe:
// a missing or malformed array means "no inbounds known").
func extractInbounds(config map[string]any) []map[string]any {
	if config == nil {
		return nil
	}
	raw, ok := config["inbounds"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// inboundJSON renders one desired inbound as the xray JSON that goes into
// the core config (shape verified in 00-ADMIN/PASARGUARD-PER-DC-DESIGN.md
// against the upstream source; the panel's user management fills in the
// clients array per user, so Quantum provisions it empty).
func (p InboundPlan) inboundJSON() map[string]any {
	listen := p.Listen
	if listen == "" {
		listen = "127.0.0.1"
	}
	return map[string]any{
		"tag":      p.Tag,
		"listen":   listen,
		"port":     p.Port,
		"protocol": "vless",
		"settings": map[string]any{
			"clients":    []any{},
			"decryption": "none",
		},
		"streamSettings": map[string]any{
			"network":  "tcp",
			"security": "reality",
			"realitySettings": map[string]any{
				"dest":        p.Dest,
				"serverNames": nonNilStrings(p.SNIs),
				"privateKey":  p.PrivateKey,
				"shortIds":    nonNilStrings(p.ShortIDs),
			},
		},
		"sniffing": map[string]any{
			"enabled":      true,
			"destOverride": []string{"http", "tls"},
		},
	}
}

func nonNilStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

// ---- apply ------------------------------------------------------------------

// DCApplier is the client surface ApplyDCInbounds needs (the real
// *Client or a fake).
type DCApplier interface {
	GetCore(ctx context.Context, coreID int) (map[string]any, error)
	ModifyCore(ctx context.Context, coreID int, restartNodes bool, body CoreModifyRequest) error
	ListHosts(ctx context.Context) ([]Host, error)
	CreateHost(ctx context.Context, req HostCreateRequest) (*Host, error)
	ModifyHost(ctx context.Context, id int, req HostCreateRequest) (*Host, error)
}

// ApplyDCInbounds executes a plan against the panel: the core PUT with
// the merged config (inbounds = untouched existing + ours, including the
// non-prefixed operator ones — the PUT replaces the WHOLE config), then
// the host creates/modifies (upstream validates the inbound_tag, so hosts
// only go out after a successful core PUT). NEVER deletes: stale rows are
// the caller's report, not an action. Returns per-step outcome rows for
// the API response (they carry no secret material).
//
// The core record is RE-READ here, not taken from the caller: the PUT is
// a full replace, and plans can age (several servers share one core — a
// plan built before a sibling apply must not wipe its writes).
func ApplyDCInbounds(ctx context.Context, ap DCApplier, coreID int, plan *DCInboundPlan) ([]string, error) {
	if plan == nil {
		return nil, errors.New("pasarguard: nil inbound plan")
	}
	results := []string{}

	// Nothing to change on the core (hosts-only drift): skip the PUT and
	// the restart it triggers.
	if len(plan.Create) == 0 && len(plan.Modify) == 0 {
		return applyDCHosts(ctx, ap, plan, results)
	}

	core, err := ap.GetCore(ctx, coreID)
	if err != nil {
		return results, fmt.Errorf("core %d read failed: %v", coreID, err)
	}
	config, _ := core["config"].(map[string]any)
	if config == nil {
		return results, fmt.Errorf("core %d: config missing or not an object", coreID)
	}
	merged, err := mergedDCConfig(config, plan)
	if err != nil {
		return results, err
	}
	body := CoreModifyRequest{
		Name:                 coreString(core, "name"),
		Config:               merged,
		Type:                 coreString(core, "type"),
		ExcludeInboundTags:   coreStrings(core, "exclude_inbound_tags"),
		FallbacksInboundTags: coreStrings(core, "fallbacks_inbound_tags"),
	}
	if err := ap.ModifyCore(ctx, coreID, true, body); err != nil {
		return results, fmt.Errorf("core %d update failed: %v", coreID, err)
	}
	results = append(results, fmt.Sprintf("core %d updated (restart_nodes=true, inbounds=%d)", coreID, len(extractInbounds(merged))))

	return applyDCHosts(ctx, ap, plan, results)
}

// applyDCHosts runs the host half of the plan. Counterparts are matched
// against the CURRENT host list (fresh read: plans age), so a host that
// appeared after the diff turns the create row into a modify and a host
// that vanished turns the modify row into a create — both reach the same
// desired state.
func applyDCHosts(ctx context.Context, ap DCApplier, plan *DCInboundPlan, results []string) ([]string, error) {
	hosts, err := ap.ListHosts(ctx)
	if err != nil {
		return results, fmt.Errorf("host list failed: %v", err)
	}
	claimed := make([]bool, len(hosts))
	// match returns the claimed host's upstream ID (0 = none): the id goes
	// into the PUT, the claim index into the local bookkeeping. Counterpart
	// scope = the full desired remark (see DiffDCInbounds: dc-only prefixes
	// would grab other servers' hosts).
	match := func(remark string) (idx, id int, ok bool) {
		for i, h := range hosts {
			if !claimed[i] && h.Remark == remark {
				return i, h.ID, true
			}
		}
		for i, h := range hosts {
			if !claimed[i] && h.Remark != remark && strings.HasPrefix(h.Remark, remark) {
				return i, h.ID, true
			}
		}
		return -1, 0, false
	}
	put := func(want HostPlan, id int) (bool, error) {
		req := HostCreateRequest{
			Remark:     want.Remark,
			InboundTag: want.InboundTag,
			Port:       want.Port,
			Security:   "inbound_default",
		}
		if want.Address != "" {
			req.Address = []string{want.Address}
		}
		if want.SNI != "" {
			req.SNI = []string{want.SNI}
		}
		if id > 0 {
			_, err := ap.ModifyHost(ctx, id, req)
			return false, err
		}
		_, err := ap.CreateHost(ctx, req)
		return true, err
	}

	for _, want := range plan.HostsCreate {
		idx, id, ok := match(want.Remark)
		if ok {
			claimed[idx] = true
		}
		created, err := put(want, id)
		if err != nil {
			results = append(results, fmt.Sprintf("host %s: FAILED: %v", want.Remark, err))
			continue
		}
		switch {
		case created && ok:
			results = append(results, fmt.Sprintf("host %s: created (replacing vanished plan counterpart)", want.Remark))
		case created:
			results = append(results, fmt.Sprintf("host %s: created", want.Remark))
		default:
			results = append(results, fmt.Sprintf("host %s: modified (id %d)", want.Remark, id))
		}
	}
	for _, want := range plan.HostsModify {
		idx, id, ok := match(want.Remark)
		if ok {
			claimed[idx] = true
		}
		created, err := put(want, id)
		if err != nil {
			results = append(results, fmt.Sprintf("host %s: FAILED: %v", want.Remark, err))
			continue
		}
		if created {
			results = append(results, fmt.Sprintf("host %s: created (counterpart vanished since the diff)", want.Remark))
		} else {
			results = append(results, fmt.Sprintf("host %s: modified (id %d)", want.Remark, id))
		}
	}
	return results, nil
}

// mergedDCConfig deep-copies the config with "inbounds" rebuilt: the
// counterpart the diff claimed for each create/modify row is REPLACED by
// the rendered desired document. Exactly one inbound is dropped per row
// (the diff's claim logic, replayed) — stale entries pass through
// untouched, and with them every non-prefixed operator inbound.
func mergedDCConfig(config map[string]any, plan *DCInboundPlan) (map[string]any, error) {
	raw, ok := config["inbounds"].([]any)
	if !ok {
		return nil, errors.New("pasarguard: core config has no inbounds array")
	}
	changes := append(append([]InboundPlan{}, plan.Create...), plan.Modify...)
	inbounds := extractInbounds(config)
	claimed := make([]bool, len(inbounds))
	drop := make([]bool, len(raw))
	for _, c := range changes {
		idx := matchInbound(inbounds, claimed, c.Tag)
		if idx < 0 {
			continue // pure create: nothing to replace
		}
		claimed[idx] = true
		drop[idx] = true
	}
	out := make([]any, 0, len(raw)+len(changes))
	for i, v := range raw {
		if !drop[i] {
			out = append(out, v)
		}
	}
	for _, c := range changes {
		out = append(out, c.inboundJSON())
	}
	merged := make(map[string]any, len(config))
	for k, v := range config {
		merged[k] = v
	}
	merged["inbounds"] = out
	return merged, nil
}

func coreString(core map[string]any, key string) string {
	s, _ := core[key].(string)
	return s
}

func coreStrings(core map[string]any, key string) []string {
	out, _ := stringSlice(core[key])
	return out
}
