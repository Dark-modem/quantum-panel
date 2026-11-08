package panel

// Entry point: config load, route table, static assets.
// Auth lives in auth.go, tunnel status/actions in tunnel.go,
// setup in setup.go, dashboard metrics in dashboard.go.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

//go:embed index.html app.js i18n-fa.js matrix_edit.js topology.js tunnel_overview.js fonts.css tokens.css base.css enterprise.css quantum-mesh.css favicon.png favicon.svg apple-touch-icon.png xterm.js xterm-fit.js xterm-search.js xterm.css fonts/vazirmatn.woff2 fonts/OFL.txt fonts/SOURCE.txt mesh.html mesh.js wizard.html wizard.js
var panelFS embed.FS

var configDir = "/etc/gre-panel"

// frpDir / backhaulDir hold the FRP and Backhaul configs; tests point them at
// throwaway dirs. Overridable with GRE_FRP_DIR / GRE_BACKHAUL_DIR.
var (
	frpDir      = "/etc/frp"
	backhaulDir = "/etc/backhaul"
)

// runSystemctl is the one place the panel shells out to systemctl for tunnel
// engine switches and port rewrites (swappable for tests).
var runSystemctl = func(args ...string) error {
	return exec.Command("systemctl", args...).Run()
}

type panelConfig struct {
	Username string `json:"username"`
	PassHash string `json:"pass_hash"`
	Port     int    `json:"port"`
	BasePath string `json:"base_path"`
	// TerminalEnabled gates the Phase 3 interactive terminal tab
	// (feature flag, default off; user enables after testing).
	TerminalEnabled bool `json:"terminal_enabled,omitempty"`
	// TerminalAllowIPs limits who may open the shell (IPs/CIDRs; empty = any).
	TerminalAllowIPs []string `json:"terminal_allow_ips,omitempty"`
	// TLSPort is the HTTPS listener port (default 7443). HTTP stays on Port.
	TLSPort int `json:"tls_port,omitempty"`
	// BindHost restricts both listeners to one address (e.g. "127.0.0.1"
	// behind a reverse proxy). Empty binds all interfaces (default).
	BindHost string `json:"bind_host,omitempty"`
	// HTTPBindHost restricts ONLY the plain-HTTP listener. Empty falls
	// back to BindHost, else 127.0.0.1 (SECURITY-AUDIT-PANEL H1: no
	// cleartext listener on a public interface by default). The env
	// GRE_PANEL_HTTP_BIND overrides both for containers/units.
	HTTPBindHost string `json:"http_bind_host,omitempty"`
	// LegacyEnabled turns the legacy host-command surface (matrix,
	// tunnel/peers/watchdog/carrier/xray/limits/revpath/swap/setup/
	// terminal commands exec'd on THIS host) back on. DISABLED by default
	// (SECURITY-AUDIT-PANEL M3) — the mesh API and agent management are
	// unaffected. Env GRE_PANEL_LEGACY=1 overrides.
	LegacyEnabled bool `json:"legacy_enabled,omitempty"`
	// PortsFinalized marks that the panel has successfully bound its
	// HTTP+TLS ports once. After that the ports (and the base path) are
	// FROZEN: a busy port at a later start is a loud fatal error, never
	// an auto-switch — an auto-switch would permanently change the URL
	// every enrolled agent already uses (PANEL-LISTENER-NOTES.md).
	PortsFinalized bool `json:"ports_finalized,omitempty"`
	// DebugEnabled mounts /debug/pprof behind auth (also QUANTUM_PPROF=1).
	DebugEnabled bool `json:"debug_enabled,omitempty"`
	// RevPathAuto enables automatic application of reverse-path fixes
	// (rate-limited; report-only when false, the default).
	RevPathAuto bool `json:"revpath_auto,omitempty"`
	// Optional TOTP 2FA (off by default). Secret and recovery hashes live in
	// panel.json (0600) like the password hash.
	// IdleTimeoutMin logs a session out after this many minutes without user
	// activity (0 = off, the default).
	IdleTimeoutMin int `json:"idle_timeout_min,omitempty"`
	// MetricsToken enables GET /metrics (Prometheus) for scrapers using a
	// bearer token; empty = endpoint disabled (404).
	MetricsToken   string   `json:"metrics_token,omitempty"`
	TOTPEnabled    bool     `json:"totp_enabled,omitempty"`
	TOTPSecret     string   `json:"totp_secret,omitempty"`
	RecoveryHashes []string `json:"recovery_hashes,omitempty"`
}

func termEnabled() bool { return cfg.TerminalEnabled }

// effectiveTLSPort returns the HTTPS port (7443 default when unset).
func effectiveTLSPort() int {
	// Explicit operator override first (symmetric with GRE_PANEL_PORT).
	if v := strings.TrimSpace(os.Getenv("GRE_PANEL_TLS_PORT")); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p >= 1 && p <= 65535 {
			return p
		}
		log.Printf("ignoring invalid GRE_PANEL_TLS_PORT=%q (must be 1-65535)", v)
	}
	if cfg.TLSPort < 1 || cfg.TLSPort > 65535 {
		return 7443
	}
	return cfg.TLSPort
}

var (
	cfg panelConfig
	// panelVersion is set at release build time:
	// go build -ldflags "-X main.panelVersion=v1.29.0"
	panelVersion = "v1.29.0"
	// panelMux is shared between the HTTP listener and the HTTPS listener.
	panelMux *http.ServeMux
)

func cfgPath() string { return filepath.Join(configDir, "panel.json") }

var (
	cfgFileModTime time.Time
	cfgFileMu      sync.RWMutex
	// cfgLoaded is set once loadOrInit has populated cfg; saving before that
	// would overwrite panel.json with zero values.
	cfgLoaded atomic.Bool
)

func recordLoadedModTime() {
	if fi, err := os.Stat(cfgPath()); err == nil {
		cfgFileMu.Lock()
		cfgFileModTime = fi.ModTime()
		cfgFileMu.Unlock()
	}
}

func reloadConfigIfChanged() {
	p := cfgPath()
	fi, err := os.Stat(p)
	if err != nil {
		return
	}
	cfgFileMu.RLock()
	lastMod := cfgFileModTime
	cfgFileMu.RUnlock()

	if !fi.ModTime().After(lastMod) {
		return
	}

	cfgFileMu.Lock()
	defer cfgFileMu.Unlock()
	if !fi.ModTime().After(cfgFileModTime) {
		return
	}

	data, err := os.ReadFile(p)
	if err != nil || len(data) == 0 {
		return
	}
	var parsed panelConfig
	if err := json.Unmarshal(data, &parsed); err == nil && strings.TrimSpace(parsed.PassHash) != "" {
		mu.Lock()
		cfg.PassHash = parsed.PassHash
		if strings.TrimSpace(parsed.Username) != "" {
			cfg.Username = parsed.Username
		}
		mu.Unlock()
		cfgFileModTime = fi.ModTime()
		log.Printf("panel credentials reloaded from disk: user=%s", cfg.Username)
	}
}

func saveCfg() {
	_ = saveConfigAtomic(cfg)
}

func saveConfigAtomic(c panelConfig) error {
	p := cfgPath()
	// Prevent accidental credential or config wipe if empty fields passed
	if existingData, err := os.ReadFile(p); err == nil && len(existingData) > 0 {
		var prev panelConfig
		if json.Unmarshal(existingData, &prev) == nil {
			if strings.TrimSpace(c.PassHash) == "" && strings.TrimSpace(prev.PassHash) != "" {
				c.PassHash = prev.PassHash
			}
			if strings.TrimSpace(c.BasePath) == "" && strings.TrimSpace(prev.BasePath) != "" {
				c.BasePath = prev.BasePath
			}
			if strings.TrimSpace(c.Username) == "" && strings.TrimSpace(prev.Username) != "" {
				c.Username = prev.Username
			}
			if c.Port <= 0 && prev.Port > 0 {
				c.Port = prev.Port
			}
		}
	}
	data := mustJSON(c)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	bak := p + ".bak"
	_ = os.WriteFile(bak, data, 0600)
	recordLoadedModTime()
	return nil
}

// ---- auto port: never fail install when the HTTP/TLS port is taken ----

// portFree reports whether TCP :port can be bound right now. It is
// bind_host aware: with bind_host set (reverse-proxy mode) it probes the
// configured host instead of all interfaces, so the panel never
// auto-switches away from a port it holds on loopback.
func portFree(port int) bool {
	ln, err := net.Listen("tcp", panelBindAddr(port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// panelBindAddr builds the listen address for the panel's listeners,
// honoring panel.json bind_host (reverse-proxy mode: "127.0.0.1" keeps
// the panel private behind nginx). Empty binds all interfaces.
func panelBindAddr(port int) string {
	host := strings.TrimSpace(cfg.BindHost)
	if host == "" {
		return fmt.Sprintf(":%d", port)
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// panelHTTPBindAddr builds the listen address for the PLAIN-HTTP
// listener. Unlike HTTPS (public by design), plain HTTP defaults to
// 127.0.0.1: this listener carries session login over cleartext and the
// panel can reconfigure root-level agents. A wider bind is always an
// explicit operator choice, in decreasing precedence:
//   - GRE_PANEL_HTTP_BIND env (containers/units: "0.0.0.0"),
//   - panel.json http_bind_host (just this listener, e.g. a cluster
//     tunnel IP so peer mesh keeps working),
//   - panel.json bind_host (both listeners, pre-existing behavior).
//
// registerUnauthRoutes wires the endpoints reachable WITHOUT any
// credential, each behind the per-IP limiter (audit M1).
func registerUnauthRoutes(mux *http.ServeMux, base string) {
	mux.HandleFunc("GET "+base+"/api/health", limitUnauth(handleHealth))
	mux.HandleFunc("POST "+base+"/api/agent/enroll", limitUnauth(handleAgentEnroll))
	mux.HandleFunc("GET "+base+"/api/agent/artifact/{name}", limitUnauth(handleAgentArtifactGet))
}

func panelHTTPBindAddr(port int) string {
	host := strings.TrimSpace(os.Getenv("GRE_PANEL_HTTP_BIND"))
	if host == "" {
		host = strings.TrimSpace(cfg.HTTPBindHost)
	}
	if host == "" {
		host = strings.TrimSpace(cfg.BindHost)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// pickFreePort scans upward from start for the first bindable TCP port
// (max 100 tries). Falls back to start so the caller errors naturally.
func pickFreePort(start int) int {
	for p := start; p < start+100 && p <= 65535; p++ {
		if p < 1 {
			continue
		}
		if portFree(p) {
			return p
		}
	}
	return start
}

// envPanelPort reads GRE_PANEL_PORT (set by quantum.sh or the admin).
// Returns 0 when unset/invalid.
func envPanelPort() int {
	v := strings.TrimSpace(os.Getenv("GRE_PANEL_PORT"))
	if v == "" {
		return 0
	}
	p, err := strconv.Atoi(v)
	if err != nil || p < 1 || p > 65535 {
		log.Printf("ignoring invalid GRE_PANEL_PORT=%q (must be 1-65535)", v)
		return 0
	}
	return p
}

// ensureFreeHTTPPort resolves the HTTP listen port. Semantics
// (PANEL-LISTENER-NOTES.md, phase 6 fix for the mid-run rebind):
//   - the operator's explicit choice always wins: GRE_PANEL_PORT env,
//     else the saved port, else 7777 on the very first start;
//   - FIRST start (ports not finalized yet) with the preferred port
//     busy: scan upward, PERSIST the new port, log it loudly — one
//     auto-switch, ever;
//   - every later start: the port is FROZEN. A busy port is a fatal
//     error with fix instructions — silently moving the port would
//     permanently break the URL every enrolled agent already uses.
func ensureFreeHTTPPort() {
	desired := cfg.Port
	if p := envPanelPort(); p != 0 {
		desired = p
	} else if desired < 1 || desired > 65535 {
		desired = 7777
	}
	if portFree(desired) {
		if cfg.Port != desired {
			cfg.Port = desired
			saveCfg()
		}
		finalizePorts()
		return
	}
	if !listenerMoveAllowed() {
		log.Fatalf("panel HTTP port %d is busy and the listener ports are FINALIZED — "+
			"the panel will NOT auto-switch (enrolled agents already use this URL). "+
			"Free the port, or set GRE_PANEL_PORT / panel.json port to another value explicitly.", desired)
	}
	next := pickFreePort(desired + 1)
	log.Printf("FIRST START: panel port %d busy — auto-switched to %d and SAVED to panel.json (ports finalize after this bind; later starts refuse to move)", desired, next)
	cfg.Port = next
	saveCfg()
	finalizePorts()
}

// listenerMoveAllowed reports whether an auto-switch is permitted right
// now: only BEFORE the ports have been finalized (the very first start).
func listenerMoveAllowed() bool { return !cfg.PortsFinalized }

// finalizePorts freezes the resolved ports: after the first successful
// bind the panel never auto-switches again (see PANEL-LISTENER-NOTES.md).
func finalizePorts() {
	if cfg.PortsFinalized {
		return
	}
	cfg.PortsFinalized = true
	saveCfg()
	log.Printf("listener ports finalized: HTTP %d, TLS %d, base path /%s (they now change only when you edit them explicitly)", cfg.Port, effectiveTLSPort(), cfg.BasePath)
}

func loadOrInit() {
	defer cfgLoaded.Store(true)
	_ = os.MkdirAll(configDir, 0700)

	// CWE-256 Migration: Delete legacy plaintext panel.pass if it exists on disk
	legacyPassFile := filepath.Join(configDir, "panel.pass")
	if _, err := os.Stat(legacyPassFile); err == nil {
		_ = os.Remove(legacyPassFile)
		log.Printf("Security migration (CWE-256): deleted legacy plaintext password file %s", legacyPassFile)
	}

	primaryPath := cfgPath()
	data, err := os.ReadFile(primaryPath)
	if err != nil || len(data) == 0 {
		bakPath := filepath.Join(configDir, "panel.json.bak")
		if bakData, bakErr := os.ReadFile(bakPath); bakErr == nil && len(bakData) > 0 {
			data = bakData
			err = nil
			log.Printf("recovered config from backup %s", bakPath)
		}
	}
	if err != nil || len(data) == 0 {
		// Fallback check for config.json if panel.json does not exist
		altPath := filepath.Join(configDir, "config.json")
		if altData, altErr := os.ReadFile(altPath); altErr == nil && len(altData) > 0 {
			data = altData
			err = nil
			log.Printf("migrating config from %s to %s", altPath, primaryPath)
		}
	}

	var existingBasePath string
	var existingPort int
	var existingUser string

	extractDefaults := func(src []byte) {
		var raw map[string]any
		if json.Unmarshal(src, &raw) == nil {
			if v, ok := raw["base_path"].(string); ok && strings.TrimSpace(v) != "" && existingBasePath == "" {
				existingBasePath = strings.TrimSpace(v)
			}
			if v, ok := raw["port"].(float64); ok && v >= 1 && v <= 65535 && existingPort == 0 {
				existingPort = int(v)
			}
			if v, ok := raw["username"].(string); ok && strings.TrimSpace(v) != "" && existingUser == "" {
				existingUser = strings.TrimSpace(v)
			}
		}
	}

	if len(data) > 0 {
		extractDefaults(data)
	}
	if existingBasePath == "" {
		if bData, bErr := os.ReadFile(filepath.Join(configDir, "panel.json.bak")); bErr == nil {
			extractDefaults(bData)
		}
	}

	if err == nil && len(data) > 0 {
		var parsed panelConfig
		if json.Unmarshal(data, &parsed) == nil && strings.TrimSpace(parsed.PassHash) != "" {
			cfg = parsed
			if strings.TrimSpace(cfg.Username) == "" {
				cfg.Username = "admin"
			}
			if cfg.Port <= 0 || cfg.Port > 65535 {
				cfg.Port = 7777
			}
			if strings.TrimSpace(cfg.BasePath) == "" {
				if existingBasePath != "" {
					cfg.BasePath = existingBasePath
				} else {
					cfg.BasePath = "panel"
				}
			}
			// Test/dev override: fixed password via env (takes effect on restart).
			if pw := os.Getenv("GRE_PANEL_PASSWORD"); pw != "" {
				cfg.PassHash = hashPassword(pw)
				saveCfg()
				log.Printf("panel password updated from GRE_PANEL_PASSWORD environment variable")
			}
			recordLoadedModTime()
			return
		}

		// Resilient fallback: rescue credentials if fields differed or minor format issue
		var raw map[string]any
		if json.Unmarshal(data, &raw) == nil {
			passHash := ""
			for _, k := range []string{"pass_hash", "password", "hash", "admin_pass", "admin_password"} {
				if v, ok := raw[k].(string); ok && strings.TrimSpace(v) != "" {
					passHash = strings.TrimSpace(v)
					break
				}
			}
			if passHash != "" {
				username := "admin"
				if existingUser != "" {
					username = existingUser
				}
				port := 7777
				if existingPort != 0 {
					port = existingPort
				}
				basePath := "panel"
				if existingBasePath != "" {
					basePath = existingBasePath
				}

				if !strings.HasPrefix(passHash, "argon2id$") && !strings.HasPrefix(passHash, "$argon2id$") && len(passHash) != 64 {
					passHash = hashPassword(passHash)
				}

				cfg = panelConfig{
					Username: username,
					PassHash: passHash,
					Port:     port,
					BasePath: basePath,
				}
				saveCfg()
				recordLoadedModTime()
				log.Printf("rescued panel credentials from config (user: %s, port: %d, base: %s)", cfg.Username, cfg.Port, cfg.BasePath)
				return
			}
		}
	}

	pass := os.Getenv("GRE_PANEL_PASSWORD")
	if pass == "" {
		pass = SecureRandomPassword(16)
	}

	basePath := existingBasePath
	if basePath == "" {
		basePath = randomBase(12)
	}
	port := 7777
	if existingPort != 0 {
		port = existingPort
	}
	user := "admin"
	if existingUser != "" {
		user = existingUser
	}

	cfg = panelConfig{
		Username: user,
		PassHash: hashPassword(pass),
		Port:     port,
		BasePath: basePath,
	}
	saveCfg()
	recordLoadedModTime()
	log.Printf("==================================================")
	log.Printf("INITIAL PANEL PASSWORD: %s (user: %s, base: /%s)", pass, cfg.Username, cfg.BasePath)
	log.Printf("Please record this password now. Plaintext is not stored on disk.")
	log.Printf("==================================================")
}

func mustJSON(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
}

func randomBase(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

func getOutboundIP() string {
	conn, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	localAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || localAddr.IP == nil {
		return "127.0.0.1"
	}
	return localAddr.IP.String()
}

func cliSetPassword(args []string) int {
	if v := os.Getenv("GRE_PANEL_DIR"); v != "" {
		configDir = v
	}
	loadOrInit()

	newPass := ""
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		newPass = strings.TrimSpace(args[0])
	} else {
		newPass = SecureRandomPassword(16)
	}

	if len(newPass) < 8 {
		fmt.Fprintf(os.Stderr, "Error: password must be at least 8 characters\n")
		return 1
	}

	mu.Lock()
	cfg.PassHash = hashPassword(newPass)
	saveCfg()
	mu.Unlock()

	ResetAllLockouts()

	ip := getOutboundIP()
	fmt.Println("==================================================")
	fmt.Println("[OK] Panel password updated.")
	fmt.Printf("Username:  %s\n", cfg.Username)
	fmt.Printf("Password:  %s\n", newPass)
	fmt.Printf("Base Path: /%s (UNCHANGED)\n", cfg.BasePath)
	fmt.Printf("Login URL: http://%s:%d/%s/\n", ip, cfg.Port, cfg.BasePath)
	fmt.Println("==================================================")
	return 0
}

func cliShowInfo() int {
	if v := os.Getenv("GRE_PANEL_DIR"); v != "" {
		configDir = v
	}
	loadOrInit()
	ip := getOutboundIP()
	fmt.Println("==================================================")
	fmt.Println("Quantum GRE-FRP Web Panel Info")
	fmt.Printf("Username:  %s\n", cfg.Username)
	fmt.Printf("Port:      %d\n", cfg.Port)
	fmt.Printf("Base Path: /%s\n", cfg.BasePath)
	fmt.Printf("Login URL: http://%s:%d/%s/\n", ip, cfg.Port, cfg.BasePath)
	fmt.Println("==================================================")
	return 0
}
func validateConfigDirs() {
	// Check and create directories if they don't exist, but only warn if we cannot create them.
	// We don't want to fail the panel if the directories are missing, but we should log.
	for _, dir := range []string{configDir, frpDir, backhaulDir} {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			if err := os.MkdirAll(dir, 0700); err != nil {
				log.Printf("WARNING: Failed to create directory %s: %v", dir, err)
			} else {
				log.Printf("INFO: Created missing directory %s", dir)
			}
		}
	}
}

func Main(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "--version", "-v", "version":
			fmt.Println(panelVersion)
			return 0
		case "--help", "-h", "help":
			fmt.Println("Usage: gre-panel [backup --out F --passphrase-file F | restore --in F --passphrase-file F [--force] | password <new-password> | info | version | tls [status|issue|renew|remove|ip|custom] | settings [show|2fa off|terminal on|off|idle N] | doctor [check|fix] | tls-proxy | pathtest | cluster [status|pool|member|join-code|add-hub|events|rules|reset] | firewall [detect|snapshot|plan|apply|teardown] | ports [list|check]]")
			return 0
		case "tls-proxy":
			os.Exit(tlsProxyCmd(args[1:]))
		case "matrix-local-proxy":
			os.Exit(matrixLocalProxyCmd(args[1:]))
		case "bagher-carrier":
			os.Exit(bagherCarrierCmd(args[1:]))
		case "pathtest":
			os.Exit(cliPathTest(args[1:]))
		case "cluster", "mesh":
			os.Exit(cliCluster(args[1:]))
		case "backup":
			return BackupCmd(args[1:])
		case "restore":
			return RestoreCmd(args[1:])
		case "password", "pass", "set-password":
			os.Exit(cliSetPassword(args[1:]))
		case "info", "status", "url":
			os.Exit(cliShowInfo())
		case "tls":
			os.Exit(cliTLS(args[1:]))
		case "settings":
			os.Exit(cliSettings(args[1:]))
		case "doctor":
			os.Exit(cliDoctor(args[1:]))
		case "netopt":
			// Network Optimization subsystem (Session 2): measure, plan,
			// apply/rollback owned tuning. See docs/NETOPT-INVENTORY.md.
			// Never touches SSH, firewall or ports (Sessions 1/6 own them).
			os.Exit(cliNetOpt(args[1:]))
		case "firewall", "ports":
			// Firewall & port engine CLI (Session 1): plan is dry-run,
			// apply/teardown are --yes-gated; all output is JSON.
			os.Exit(firewallCLI(args[0], args[1:]))
		default:
			// Unknown subcommand: fail fast with usage. Falling through
			// would silently start a full second panel instance and look
			// exactly like a hang to any caller probing a subcommand this
			// binary does not have (the reported installer "detect hang").
			fmt.Fprintf(os.Stderr, "unknown command %q — see: gre-panel --help\n", args[0])
			os.Exit(2)
		}
	}
	if v := os.Getenv("GRE_PANEL_DIR"); v != "" {
		configDir = v
	}
	if v := os.Getenv("GRE_FRP_DIR"); v != "" {
		frpDir = v
	}
	if v := os.Getenv("GRE_BACKHAUL_DIR"); v != "" {
		backhaulDir = v
	}
	if v := os.Getenv("GRE_SYSCTL_CONF"); v != "" {
		revpathSysctlConf = v
	}
	validateConfigDirs()
	loadOrInit()
	wireAgentSubsystem()
	ensureFreeHTTPPort()
	if n, err := upgradeBagherKeyLocks(); err != nil {
		log.Printf("bagher: upgrading key restrictions in %s: %v", authorizedKeysPath, err)
	} else if n > 0 {
		log.Printf("bagher: locked down %d Bagher key(s) in %s (no commands, no -L forwards)", n, authorizedKeysPath)
	}
	if ok, err := migrateBagherFRPCTLS("/etc/gre-panel/bagher/frpc.toml"); err != nil {
		log.Printf("bagher: turning off frp TLS inside the SSH tunnel: %v", err)
	} else if ok {
		log.Printf("bagher: frp TLS turned off inside the SSH tunnel (it encrypted everything twice)")
	}
	if ok, err := migrateBagherCarrierUnit(bagherLinkUnitPath(1)); err != nil {
		log.Printf("bagher: moving the carrier off ssh -N: %v", err)
	} else if ok {
		log.Printf("bagher: the carrier now runs in the panel (ssh -N held small packets back: high ping)")
	}
	if _, err := rand.Read(nonce[:]); err != nil {
		log.Fatal(err)
	}

	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		nonceTicker := time.NewTicker(12 * time.Hour)
		defer nonceTicker.Stop()
		for {
			select {
			case <-ticker.C:
				reloadConfigIfChanged()
			case <-nonceTicker.C:
				rotateNonce()
			}
		}
	}()

	base := "/" + cfg.BasePath
	mux := http.NewServeMux()
	panelMux = mux
	mux.HandleFunc("GET "+base+"/", serveIndex)
	mux.HandleFunc("GET "+base+"/tunnel_overview.js", serveAsset("tunnel_overview.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET "+base+"/topology.js", serveAsset("topology.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET "+base+"/matrix_edit.js", serveAsset("matrix_edit.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET "+base+"/app.js", serveAsset("app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET "+base+"/i18n-fa.js", serveAsset("i18n-fa.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET "+base+"/tokens.css", serveAsset("tokens.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET "+base+"/fonts.css", serveAsset("fonts.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET "+base+"/fonts/vazirmatn.woff2", serveAsset("fonts/vazirmatn.woff2", "font/woff2"))
	mux.HandleFunc("GET "+base+"/base.css", serveAsset("base.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET "+base+"/enterprise.css", serveAsset("enterprise.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET "+base+"/quantum-mesh.css", serveAsset("quantum-mesh.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET "+base+"/favicon.ico", serveAsset("favicon.png", "image/png"))
	mux.HandleFunc("GET "+base+"/favicon.png", serveAsset("favicon.png", "image/png"))
	mux.HandleFunc("GET "+base+"/favicon.svg", serveAsset("favicon.svg", "image/svg+xml"))
	mux.HandleFunc("GET "+base+"/apple-touch-icon.png", serveAsset("apple-touch-icon.png", "image/png"))
	mux.HandleFunc("GET "+base+"/xterm.js", serveAsset("xterm.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET "+base+"/xterm-fit.js", serveAsset("xterm-fit.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET "+base+"/xterm-search.js", serveAsset("xterm-search.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET "+base+"/xterm.css", serveAsset("xterm.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET "+base+"/api/term/ws", requireAuth(handleTermWS))
	mux.HandleFunc("GET "+base+"/api/term/status", requireAuth(handleTermStatus))
	mux.HandleFunc("POST "+base+"/api/term/ticket", requireAuth(requireCSRF(handleTermTicket)))
	mux.HandleFunc("GET "+base+"/api/term/allowlist", requireAuth(handleTermAllowGet))
	mux.HandleFunc("POST "+base+"/api/term/allowlist", requireAuth(requireCSRF(handleTermAllowPost)))
	mux.HandleFunc("POST "+base+"/api/term/kill", requireAuth(requireCSRF(handleTermKill)))
	mux.HandleFunc("POST "+base+"/api/term/enable", requireAuth(requireCSRF(handleTermEnable)))
	registerUnauthRoutes(mux, base)
	mux.HandleFunc("GET "+base+"/api/status", requireAuth(handleStatus))
	mux.HandleFunc("GET "+base+"/api/dashboard", requireAuth(handleDashboard))
	mux.HandleFunc("POST "+base+"/api/login", handleLogin)
	mux.HandleFunc("POST "+base+"/api/logout", handleLogout)
	mux.HandleFunc("GET "+base+"/api/logs", requireAuth(handleLogs))
	mux.HandleFunc("GET "+base+"/api/errors", requireAuth(handleErrors))
	mux.HandleFunc("GET "+base+"/api/audit", requireAuth(handleAuditGet))
	mux.HandleFunc("GET "+base+"/api/access", requireAuth(handleAccessGet))
	mux.HandleFunc("POST "+base+"/api/access", requireAuth(requireCSRF(handleAccessPost)))
	mux.HandleFunc("GET "+base+"/api/session-settings", requireAuth(handleSessionSettingsGet))
	mux.HandleFunc("POST "+base+"/api/session-settings", requireAuth(requireCSRF(handleSessionSettingsPost)))
	mux.HandleFunc("GET "+base+"/api/alerts", requireAuth(handleAlertsGet))
	mux.HandleFunc("POST "+base+"/api/alerts", requireAuth(requireCSRF(handleAlertsPost)))
	mux.HandleFunc("GET "+base+"/api/history", requireAuth(handleHistoryGet))
	mux.HandleFunc("GET "+base+"/api/traffic/usage", requireAuth(handleUsageGet))
	mux.HandleFunc("GET "+base+"/metrics", handleMetrics)
	mux.HandleFunc("GET "+base+"/api/metrics-token", requireAuth(handleMetricsTokenGet))
	mux.HandleFunc("POST "+base+"/api/metrics-token", requireAuth(requireCSRF(handleMetricsTokenPost)))
	mux.HandleFunc("GET "+base+"/api/carrier/advice", requireAuth(handleCarrierAdvice))
	mux.HandleFunc("GET "+base+"/api/suggest", requireAuth(handleSuggest))
	mux.HandleFunc("GET "+base+"/api/2fa", requireAuth(handle2FAStatus))
	mux.HandleFunc("POST "+base+"/api/2fa/setup", requireAuth(requireCSRF(handle2FASetup)))
	mux.HandleFunc("POST "+base+"/api/2fa/enable", requireAuth(requireCSRF(handle2FAEnable)))
	mux.HandleFunc("POST "+base+"/api/2fa/disable", requireAuth(requireCSRF(handle2FADisable)))
	mux.HandleFunc("POST "+base+"/api/action", requireAuth(requireCSRF(handleAction)))
	mux.HandleFunc("POST "+base+"/api/password", requireAuth(requireCSRF(handlePassword)))
	mux.HandleFunc("GET "+base+"/api/setup", requireAuth(handleSetupGet))
	mux.HandleFunc("GET "+base+"/api/version", requireAuth(handleVersion))
	mux.HandleFunc("POST "+base+"/api/update", requireAuth(requireCSRF(handleUpdate)))
	mux.HandleFunc("GET "+base+"/api/update/channel", requireAuth(handleUpdateChannelGet))
	mux.HandleFunc("POST "+base+"/api/update/channel", requireAuth(requireCSRF(handleUpdateChannelPost)))
	mux.HandleFunc("GET "+base+"/api/releases", requireAuth(handleReleases))
	mux.HandleFunc("GET "+base+"/api/update/github-token", requireAuth(handleGithubTokenGet))
	mux.HandleFunc("POST "+base+"/api/update/github-token", requireAuth(requireCSRF(handleGithubTokenPost)))
	mux.HandleFunc("POST "+base+"/api/matrix/setup", requireAuth(requireLegacy(requireCSRF(handleMatrixSetupPost))))
	mux.HandleFunc("GET "+base+"/api/matrix/status", requireAuth(handleMatrixStatus))
	mux.HandleFunc("GET "+base+"/api/matrix/instances", requireAuth(handleMatrixInstancesGet))
	mux.HandleFunc("POST "+base+"/api/matrix/remove", requireAuth(requireLegacy(requireCSRF(handleMatrixRemovePost))))
	mux.HandleFunc("POST "+base+"/api/matrix/restart", requireAuth(requireLegacy(requireCSRF(handleMatrixRestartPost))))
	mux.HandleFunc("POST "+base+"/api/matrix/foreign-setup", requireAuth(requireLegacy(requireCSRF(handleMatrixForeignSetup))))
	mux.HandleFunc("POST "+base+"/api/matrix/foreign-remove", requireAuth(requireLegacy(requireCSRF(handleMatrixForeignRemove))))
	mux.HandleFunc("GET "+base+"/api/matrix/local-service", requireAuth(handleMatrixLocalService))
	mux.HandleFunc("POST "+base+"/api/matrix/local-service", requireAuth(requireLegacy(requireCSRF(handleMatrixLocalService))))
	mux.HandleFunc("GET "+base+"/api/topologies", requireAuth(handleTopologiesGet))
	mux.HandleFunc("POST "+base+"/api/topologies", requireAuth(requireCSRF(handleTopologiesPost)))
	mux.HandleFunc("PATCH "+base+"/api/topologies/{id}", requireAuth(requireCSRF(handleTopologyPatch)))
	mux.HandleFunc("DELETE "+base+"/api/topologies/{id}", requireAuth(requireCSRF(handleTopologyDelete)))
	mux.HandleFunc("POST "+base+"/api/topologies/{id}/active", requireAuth(requireCSRF(handleTopologyPowerPost)))
	mux.HandleFunc("POST "+base+"/api/matrix/edit", requireAuth(requireLegacy(requireCSRF(handleMatrixEdit))))
	mux.HandleFunc("POST "+base+"/api/matrix/register", requireLegacy(handleMatrixRegister))

	// Agents: central controller <-> lightweight endpoint agents. The
	// enroll + WS routes authenticate with agent credentials, not sessions.

	mux.HandleFunc("GET "+base+"/api/agent/ws", handleAgentWS)
	mux.HandleFunc("GET "+base+"/api/agents", requireAuth(handleAgentsGet))
	mux.HandleFunc("POST "+base+"/api/agents/tokens", requireAuth(requireCSRF(handleAgentTokenPost)))
	mux.HandleFunc("GET "+base+"/api/agents/tokens", requireAuth(handleAgentTokensGet))
	mux.HandleFunc("POST "+base+"/api/agents/tokens/{id}/revoke", requireAuth(requireCSRF(handleAgentTokenRevoke)))
	mux.HandleFunc("GET "+base+"/api/agents/{id}", requireAuth(handleAgentGet))
	mux.HandleFunc("POST "+base+"/api/agents/{id}/revoke", requireAuth(requireCSRF(handleAgentRevoke)))
	mux.HandleFunc("POST "+base+"/api/agents/{id}/command", requireAuth(requireCSRF(handleAgentCommandPost)))
	mux.HandleFunc("POST "+base+"/api/agents/{id}/desired", requireAuth(requireCSRF(handleAgentDesiredPost)))
	mux.HandleFunc("POST "+base+"/api/agents/{id}/update", requireAuth(requireCSRF(handleAgentUpdatePost)))
	mux.HandleFunc("POST "+base+"/api/agents/{id}/op", requireAuth(requireCSRF(handleAgentOpPost)))
	// Agent artifact delivery: integrity is enforced by the mandatory
	// ed25519 signature + digest on both controller and agent, not by
	// transport auth (release binaries are public artifacts).

	mux.HandleFunc("GET "+base+"/api/agent-tunnels", requireAuth(handleAgentTunnelsGet))
	mux.HandleFunc("POST "+base+"/api/agent-tunnels", requireAuth(requireLegacy(requireCSRF(handleAgentTunnelsPost))))
	mux.HandleFunc("POST "+base+"/api/agent-tunnels/{id}/power", requireAuth(requireLegacy(requireCSRF(handleAgentTunnelPower))))
	mux.HandleFunc("POST "+base+"/api/agent-tunnels/{id}/restart", requireAuth(requireLegacy(requireCSRF(handleAgentTunnelRestart))))
	mux.HandleFunc("DELETE "+base+"/api/agent-tunnels/{id}", requireAuth(requireLegacy(requireCSRF(handleAgentTunnelDelete))))

	// Mesh management plane (Phase 2B): the SQLite store is the authority
	// for the new model; legacy JSON features above stay frozen and never
	// read the store. Mutations ride the same requireAuth(requireCSRF(...))
	// gate and the store's audit table records the domain events.
	registerMeshRoutes(mux, base)
	registerUIAPIRoutes(mux, base)
	// Phase 5 R5: engine artifact store + agent manifest (engineartifacts.go).
	registerEngineArtifactRoutes(mux, base)
	// Phase 5 TASK 2: encrypted backup download behind re-auth (backupapi.go).
	registerBackupRoutes(mux, base)
	// Phase 5 TASK 3: mesh alert engine + notifiers (meshalerts_*.go).
	registerMeshAlertRoutes(mux, base)
	// Phase 9 TASK 2a: engine parameter templates (enginetemplates.go).
	registerEngineTemplateRoutes(mux, base)
	mux.HandleFunc("GET "+base+"/mesh", requireAuth(serveMeshPage))
	mux.HandleFunc("GET "+base+"/mesh.js", serveAsset("mesh.js", "text/javascript; charset=utf-8"))
	// Phase 7: the /mesh setup wizard (wizard.html + wizard.js, fa/en) —
	// everything rides the existing mesh REST API; the page is just the
	// guided flow over it.
	mux.HandleFunc("GET "+base+"/mesh-wizard", requireAuth(serveWizardPage))
	mux.HandleFunc("GET "+base+"/wizard.js", serveAsset("wizard.js", "text/javascript; charset=utf-8"))

	mux.HandleFunc("GET "+base+"/api/update/peer", requireAuth(handleUpdatePeer))
	mux.HandleFunc("POST "+base+"/api/setup", requireAuth(requireLegacy(requireCSRF(handleSetupPost))))
	mux.HandleFunc("GET "+base+"/api/peers", requireAuth(handlePeersGet))
	mux.HandleFunc("POST "+base+"/api/peers", requireAuth(requireLegacy(requireCSRF(handlePeersPost))))
	mux.HandleFunc("PATCH "+base+"/api/peers", requireAuth(requireLegacy(requireCSRF(handlePeersPatch))))
	mux.HandleFunc("GET "+base+"/api/tls", requireAuth(handleTLSGet))
	mux.HandleFunc("POST "+base+"/api/tls", requireAuth(requireCSRF(handleTLSIssue)))
	mux.HandleFunc("DELETE "+base+"/api/tls", requireAuth(requireCSRF(handleTLSRemove)))
	mux.HandleFunc("POST "+base+"/api/tls/renew", requireAuth(requireCSRF(handleTLSRenew)))
	mux.HandleFunc("POST "+base+"/api/tls/check", requireAuth(requireCSRF(handleTLSCheck)))
	mux.HandleFunc("POST "+base+"/api/tls/custom", requireAuth(requireCSRF(handleTLSCustomPost)))
	mux.HandleFunc("POST "+base+"/api/tls/ip", requireAuth(requireCSRF(handleTLSIPPost)))
	mux.HandleFunc("GET "+base+"/api/tls/ip/ca", requireAuth(handleTLSIPCAGet))
	mux.HandleFunc("GET "+base+"/api/tls/proxy-config", requireAuth(handleReverseProxyConfig))
	mux.HandleFunc("GET "+base+"/api/watchdog", requireAuth(handleWatchdogGet))
	mux.HandleFunc("POST "+base+"/api/watchdog", requireAuth(requireLegacy(requireCSRF(handleWatchdogPost))))
	mux.HandleFunc("GET "+base+"/api/watchdog/backup-download", requireAuth(handleBackupDownload))
	mux.HandleFunc("POST "+base+"/api/watchdog/backup-download", requireAuth(requireCSRF(handleBackupDownload)))
	mux.HandleFunc("GET "+base+"/api/perf", requireAuth(handlePerfGet))
	mux.HandleFunc("POST "+base+"/api/perf", requireAuth(requireCSRF(handlePerfPost)))
	mux.HandleFunc("GET "+base+"/api/carrier", requireAuth(handleCarrierGet))
	mux.HandleFunc("POST "+base+"/api/carrier", requireAuth(requireLegacy(requireCSRF(handleCarrierPost))))
	mux.HandleFunc("GET "+base+"/api/tunnel/swap", requireAuth(handleSwapGet))
	mux.HandleFunc("POST "+base+"/api/tunnel/swap", requireAuth(requireLegacy(requireCSRF(handleSwapPost))))
	mux.HandleFunc("GET "+base+"/api/spoof", requireAuth(handleSpoofGet))
	mux.HandleFunc("POST "+base+"/api/spoof", requireAuth(requireCSRF(handleSpoofPost)))
	mux.HandleFunc("GET "+base+"/api/spoof/tester", requireAuth(handleSpoofTester))
	mux.HandleFunc("POST "+base+"/api/spoof/tester", requireAuth(requireCSRF(handleSpoofTester)))
	mux.HandleFunc("GET "+base+"/api/xray", requireAuth(handleXrayGet))
	mux.HandleFunc("POST "+base+"/api/xray", requireAuth(requireLegacy(requireCSRF(handleXrayPost))))
	mux.HandleFunc("GET "+base+"/api/limits", requireAuth(handleLimitsGet))
	mux.HandleFunc("POST "+base+"/api/limits", requireAuth(requireLegacy(requireCSRF(handleLimitsPost))))
	mux.HandleFunc("POST "+base+"/api/link-name", requireAuth(requireCSRF(handleLinkName)))
	mux.HandleFunc("GET "+base+"/api/fleet", requireAuth(handleFleet))
	mux.HandleFunc("GET "+base+"/api/selfstats", requireAuth(handleSelfStats))
	mux.HandleFunc("GET "+base+"/api/tunnel-health", requireAuth(handleTunnelHealthGet))
	mux.HandleFunc("POST "+base+"/api/tunnel-health/run", requireAuth(requireLegacy(requireCSRF(handleTunnelHealthRun))))
	mux.HandleFunc("GET "+base+"/api/revpath", requireAuth(handleRevPathGet))
	mux.HandleFunc("POST "+base+"/api/revpath/run", requireAuth(requireLegacy(requireCSRF(handleRevPathRun))))
	mux.HandleFunc("POST "+base+"/api/revpath/fix", requireAuth(requireLegacy(requireCSRF(handleRevPathFix))))
	mux.HandleFunc("GET "+base+"/api/revpath/fixes", requireAuth(handleRevPathFixes))
	mountDebug(mux, base)
	mux.HandleFunc("GET "+base+"/api/dial", requireAuth(handleDialGet))
	mux.HandleFunc("POST "+base+"/api/dial", requireAuth(requireCSRF(handleDialPost)))
	mux.HandleFunc("GET "+base+"/api/doctor", requireAuth(handleDoctorGet))
	mux.HandleFunc("POST "+base+"/api/doctor", requireAuth(requireCSRF(handleDoctorPost)))

	mux.HandleFunc("/", handleRootEntry)

	// Peer-to-peer synchronization & commands
	mountPeerRoutes(mux, base)
	mux.HandleFunc("GET "+base+"/api/peer/rescue-offer", requireRescuePeerAuth(handlePeerRescueOffer))
	mux.HandleFunc("POST "+base+"/api/peer/rescue-ack", requireRescuePeerAuth(handlePeerRescueAck))
	mux.HandleFunc("GET "+base+"/api/rescue", requireAuth(handleRescueGet))
	mux.HandleFunc("POST "+base+"/api/rescue", requireAuth(requireCSRF(handleRescuePost)))
	mux.HandleFunc("GET "+base+"/api/sshfwd", requireAuth(handleSSHFwdGet))
	mux.HandleFunc("POST "+base+"/api/sshfwd", requireAuth(requireCSRF(handleSSHFwdPost)))

	// Cluster: pools, linked Iran hubs, DNS failover, extra hubs of a foreign server
	mux.HandleFunc("GET "+base+"/api/cluster/status", requireAuth(handleClusterStatus))
	mux.HandleFunc("GET "+base+"/api/cluster/rules", requireAuth(handleClusterRules))
	mux.HandleFunc("POST "+base+"/api/cluster/pool", requireAuth(requireCSRF(handleClusterPool)))
	mux.HandleFunc("POST "+base+"/api/cluster/pool/delete", requireAuth(requireCSRF(handleClusterPoolDelete)))
	mux.HandleFunc("POST "+base+"/api/cluster/pool/failback", requireAuth(requireCSRF(handleClusterPoolFailback)))
	mux.HandleFunc("POST "+base+"/api/cluster/member", requireAuth(requireCSRF(handleClusterMember)))
	mux.HandleFunc("POST "+base+"/api/cluster/settings", requireAuth(requireCSRF(handleClusterSettings)))
	mux.HandleFunc("POST "+base+"/api/cluster/join-code", requireAuth(requireCSRF(handleClusterJoinCode)))
	mux.HandleFunc("POST "+base+"/api/cluster/hubs/add", requireAuth(requireCSRF(handleClusterHubAdd)))
	mux.HandleFunc("POST "+base+"/api/cluster/hubs/delete", requireAuth(requireCSRF(handleClusterHubDelete)))
	mux.HandleFunc("POST "+base+"/api/cluster/dns", requireAuth(requireCSRF(handleClusterDNS)))
	mux.HandleFunc("POST "+base+"/api/cluster/join-hub", requireAuth(requireCSRF(handleClusterJoinHub)))
	mux.HandleFunc("POST "+base+"/api/cluster/leave-hub", requireAuth(requireCSRF(handleClusterLeaveHub)))
	mountMeshRoutes(mux)
	mux.HandleFunc("GET "+base+"/api/bagher", requireAuth(handleSSHFwdGet))
	mux.HandleFunc("POST "+base+"/api/bagher", requireAuth(requireCSRF(handleSSHFwdPost)))
	mux.HandleFunc("GET "+base+"/api/peer/config", requireAuth(handlePeerConfigGet))
	mux.HandleFunc("POST "+base+"/api/peer/config", requireAuth(requireCSRF(handlePeerConfigPost)))

	// Carrier benchmark & auto-pilot
	mux.HandleFunc("GET "+base+"/api/benchmark", requireAuth(handleBenchmarkGet))
	mux.HandleFunc("POST "+base+"/api/benchmark/run", requireAuth(requireCSRF(handleBenchmarkRun)))
	mux.HandleFunc("POST "+base+"/api/benchmark/apply", requireAuth(requireCSRF(handleBenchmarkApply)))
	mux.HandleFunc("POST "+base+"/api/benchmark/autopilot", requireAuth(requireCSRF(handleBenchmarkAutoPilot)))
	// Path test: which transports cross between the two servers, before any tunnel
	mux.HandleFunc("GET "+base+"/api/pathtest", requireAuth(handlePathTestGet))
	mux.HandleFunc("POST "+base+"/api/pathtest", requireAuth(requireCSRF(handlePathTestPost)))

	addr := panelHTTPBindAddr(cfg.Port)
	log.Printf("gre-panel listening on %s under /%s", addr, cfg.BasePath)
	histStop := make(chan struct{})
	startHistoryPersistence(histStop)
	startUsagePersistence(histStop)
	startSnapshotCollector(context.Background())
	startSdWatchdog(context.Background())
	go startHTTPSListener()
	startTLSRenewWatcher()
	go startAutoPilotMonitor()
	go startPeerSyncWorker()
	go startTrafficRecorder()
	go startFleetSampler()
	go startRescueMonitor()
	go startClusterMonitor()
	go startMeshLoop()
	meshAlertEng.Run() // TASK 3: alert engine tick loop
	go startLimitsLoop()
	go func() {
		cc := loadCarrierConfig()
		if strings.HasPrefix(cc.ActiveCarrier, "wss") {
			_ = startWSSCarrier(loadWSSConfig())
		}
	}()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// ensureFreeHTTPPort already resolved (and on first start
		// persisted) the port. A failure HERE is the operator's business:
		// the port froze between resolution and bind — never auto-switch.
		log.Fatalf("panel cannot listen on %s: %v — the ports are final; free the port or set GRE_PANEL_PORT/panel.json explicitly", addr, err)
	}
	srv := &http.Server{
		// errorPagesMiddleware renders the branded Persian error page for
		// browser navigations only; API JSON and WebSocket upgrades pass
		// through untouched (innermost, inside the security headers).
		Handler:           panelChain(errorPagesMiddleware(mux)),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	applyServerLimits(srv)
	// Listening: an update that brought this binary in worked.
	clearUpdatePending()
	reportRollback()
	go pathMTUKeeper()
	go migrateLegacyBackhaul()
	// systemd stop sends SIGTERM: flush history, then stop serving.
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigc
		close(histStop)
		limitsFlush()
		time.Sleep(300 * time.Millisecond) // let the final history write finish
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	return 0
}

func serveAsset(name, ctype string) http.HandlerFunc {
	// Pre-compress text assets once at first request: on lossy Iran links,
	// xterm.js (283KB) stalled mid-transfer while small files passed through.
	// gzip shrinks it to ~70KB so the panel loads instantly.
	type blob struct {
		raw []byte
		gz  []byte
	}
	var mu sync.Mutex
	cache := map[string]*blob{}
	gzOK := func(ct string) bool {
		return strings.HasPrefix(ct, "text/") || strings.Contains(ct, "javascript") || strings.HasSuffix(ct, ".woff2") || ct == "font/woff2"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		b, ok := cache[name]
		if !ok {
			data, err := panelFS.ReadFile(name)
			mu.Unlock()
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			b = &blob{raw: data}
			if gzOK(ctype) {
				var buf bytes.Buffer
				gw := gzip.NewWriter(&buf)
				_, _ = gw.Write(data)
				_ = gw.Close()
				b.gz = buf.Bytes()
			}
			mu.Lock()
			cache[name] = b
			mu.Unlock()
		} else {
			mu.Unlock()
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if len(b.gz) > 0 && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Vary", "Accept-Encoding")
			_, _ = w.Write(b.gz)
			return
		}
		_, _ = w.Write(b.raw)
	}
}

var (
	assetTagOnce sync.Once
	assetTagVal  string
)

// assetTag is the ?v= of the first-party assets: the version plus a hash of
// their content, so a new build is fetched even under the same version (a dev
// build, a rebuilt release). i18n-fa.js reuses app.js's query.
func assetTag() string {
	assetTagOnce.Do(func() {
		h := sha256.New()
		for _, f := range []string{"app.js", "i18n-fa.js", "tokens.css", "base.css", "enterprise.css",
			"mesh.js", "quantum-mesh.css", "wizard.js"} {
			b, _ := panelFS.ReadFile(f)
			h.Write(b)
		}
		assetTagVal = panelVersion + "-" + hex.EncodeToString(h.Sum(nil)[:5])
	})
	return assetTagVal
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	if authed(r) {
		if c, err := r.Cookie("gre_session"); err == nil && c.Value != "" {
			csrfTok := GenerateCSRFToken(c.Value)
			http.SetCookie(w, csrfCookie(r, csrfTok, 86400))
		}
	}
	data, err := panelFS.ReadFile("index.html")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	page := strings.ReplaceAll(string(data), "__BASE_PATH__", "/"+cfg.BasePath)
	// Cache-bust first-party assets per build: they are served with a 24h cache.
	for _, f := range []string{"tokens.css", "base.css", "enterprise.css"} {
		page = strings.ReplaceAll(page, `href="`+f+`"`, `href="`+f+`?v=`+assetTag()+`"`)
	}
	page = strings.ReplaceAll(page, `src="tunnel_overview.js"`, `src="tunnel_overview.js?v=`+assetTag()+`"`)
	page = strings.ReplaceAll(page, `src="topology.js"`, `src="topology.js?v=`+assetTag()+`"`)
	page = strings.ReplaceAll(page, `src="matrix_edit.js"`, `src="matrix_edit.js?v=`+assetTag()+`"`)
	page = strings.ReplaceAll(page, `src="app.js"`, `src="app.js?v=`+assetTag()+`"`)
	body := []byte(page)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") && len(body) > 1024 {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		_, _ = gw.Write(body)
		_ = gw.Close()
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		_, _ = w.Write(buf.Bytes())
		return
	}
	_, _ = w.Write(body)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// handleHealth is unauthenticated: lets browsers/proxies verify the panel
// is reachable without exposing any data.
func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"status": "ok", "version": panelVersion})
}

// handleRootEntry is the catch-all for paths no route claimed. Only the
// exact root entry point redirects to the panel's secret base path — the
// operator's bookmark UX — while every OTHER unmatched path gets a plain
// 404. Redirecting arbitrary probed paths used to disclose that a panel
// lives behind this port and handed out the secret base path to anything
// that asked (audit finding F1); 404 keeps probing blind.
func handleRootEntry(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	mu.Lock()
	curBase := cfg.BasePath
	mu.Unlock()
	if curBase == "" {
		curBase = "panel"
	}
	target := "/" + curBase + "/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusFound)
}
