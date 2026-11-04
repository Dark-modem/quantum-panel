package panel

// frpSecrets is the per-install frp credential set. The Phase-1A
// decision (ARCHITECTURE-BRIEF-V2 follow-up): the old shared hardcoded
// token/password are gone — every install generates its own on first
// use and persists them 0600 (atomic tmp+rename) under configDir.
//
// Migration note: an install upgrading from the hardcoded defaults
// starts with fresh credentials, so the frp config must regenerate on
// both ends (the normal carrier/swap/enable flow already re-renders and
// reapplies configs; safe-apply + rollback cover the rollout).

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// frpDashboardUser is fixed: the user name is not a secret.
const frpDashboardUser = "admin"

// frpSecretsFile is the credentials file inside configDir.
const frpSecretsFile = "frp-secrets.json"

type frpSecrets struct {
	AuthToken         string `json:"auth_token"`
	DashboardUser     string `json:"dashboard_user"`
	DashboardPassword string `json:"dashboard_password"`
}

var (
	frpSecretsMu    sync.Mutex
	frpSecretsCache *frpSecrets
)

// loadFrpSecrets returns the install's credential set, generating and
// persisting it on first use. If the file exists it wins (0600 enforced
// best-effort); if it is unreadable or corrupt a fresh set is generated
// and written. When persistence fails (read-only host) the generated
// set is still returned so callers keep working for this process.
func loadFrpSecrets() frpSecrets {
	frpSecretsMu.Lock()
	defer frpSecretsMu.Unlock()
	if frpSecretsCache != nil {
		return *frpSecretsCache
	}
	sec := readFrpSecrets()
	if sec == nil {
		sec = &frpSecrets{
			AuthToken:         randomToken(32),
			DashboardUser:     frpDashboardUser,
			DashboardPassword: randomToken(20),
		}
		if err := saveFrpSecrets(*sec); err != nil {
			// Log-and-continue: callers still get working credentials
			// for this process lifetime.
			log.Printf("[secrets] cannot persist %s: %v", frpSecretsFile, err)
		}
	}
	frpSecretsCache = sec
	return *sec
}

func frpSecretsPath() string { return filepath.Join(configDir, frpSecretsFile) }

func readFrpSecrets() *frpSecrets {
	data, err := os.ReadFile(frpSecretsPath())
	if err != nil {
		return nil
	}
	var sec frpSecrets
	if jsonErr := json.Unmarshal(data, &sec); jsonErr != nil || sec.AuthToken == "" || sec.DashboardPassword == "" {
		return nil
	}
	return &sec
}

func saveFrpSecrets(sec frpSecrets) error {
	_ = os.MkdirAll(configDir, 0700)
	b, err := json.MarshalIndent(sec, "", "  ")
	if err != nil {
		return err
	}
	tmp := frpSecretsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, frpSecretsPath()); err != nil {
		return err
	}
	_ = os.Chmod(frpSecretsPath(), 0600)
	return nil
}

func frpAuthToken() string     { return loadFrpSecrets().AuthToken }
func frpDashboardPass() string { return loadFrpSecrets().DashboardPassword }
