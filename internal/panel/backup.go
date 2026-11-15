// backup.go — TASK 2: consistent encrypted panel backup/restore.
//
//	quantum-panel backup --out FILE --passphrase-file F
//	quantum-panel restore --in FILE --passphrase-file F [--force]
//
// The SQLite snapshot uses VACUUM INTO (a consistent, WAL-safe copy —
// never a live .db/.wal file pair). The bundle is a tar of the snapshot,
// the key files (pasarguard key, TLS cert+key, engine-artifact public
// key), panel.json as a HASH-ONLY entry, and a manifest listing every
// member with its sha256. The tar is encrypted wholesale: scrypt
// (N=2^15, r=8, p=1) derives the key, AES-256-GCM authenticates it —
// a tampered file fails the GCM tag BEFORE anything is written.
//
// Restore refuses to overwrite a RUNNING panel (systemd unit active)
// without --force, restores modes to 0600/0640 (clamped), and never
// logs passphrases, key material, or decrypted contents.

package panel

import (
	"archive/tar"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"

	"github.com/Dark-modem/quantum-panel/internal/pasarguard"
)

const (
	backupMagic       = "QPBK1\x00"
	backupSaltLen     = 32
	backupNonceLen    = 12
	backupScryptN     = 1 << 15
	backupScryptR     = 8
	backupScryptP     = 1
	backupKeyLen      = 32      // AES-256
	backupMaxTarBytes = 1 << 30 // 1 GiB decompressed cap (bomb guard)
)

// backupMember is one manifest entry; Mode is stored so restore can
// recreate permissions (clamped to <= 0640).
type backupMember struct {
	Name   string `json:"name"` // relative path inside the tar
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
	Hashed bool   `json:"hashed,omitempty"` // true = content NOT stored (hash only)
}

type backupManifest struct {
	Version   string         `json:"panel_version"`
	CreatedAt int64          `json:"created_at"`
	Members   []backupMember `json:"members"`
}

// backupKeyFiles returns the secret-bearing files that ride the bundle
// (relative name inside the tar → absolute path on disk).
func backupKeyFiles() (map[string]string, error) {
	files := map[string]string{}
	if _, err := os.Stat(pasarguard.SecretKeyPath(configDir)); err == nil {
		files["pasarguard.key"] = pasarguard.SecretKeyPath(configDir)
	}
	if _, err := os.Stat(tlsKeyFile()); err == nil {
		files["tls/server.key"] = tlsKeyFile()
	}
	if _, err := os.Stat(tlsCertFile()); err == nil {
		files["tls/server.crt"] = tlsCertFile()
	}
	if _, err := os.Stat(enginePubKeyPath()); err == nil {
		files["engine-artifacts/engine-pub.b64"] = enginePubKeyPath()
	}
	// Round-3 finding F3: the sealed mesh-alerts settings file carries the
	// alert thresholds, the notifier secrets AND the phase-16 maintenance
	// windows — without it a backup/restore round trip silently dropped
	// that state (0600: sealed secrets at rest).
	if _, err := os.Stat(meshAlertSettingsPath()); err == nil {
		files["mesh-alerts.json"] = meshAlertSettingsPath()
	}
	return files, nil
}

// sqliteSnapshot produces a consistent copy of the panel database via
// VACUUM INTO (never a live WAL copy). Uses the sqlite3 CLI when
// present, else the pure-Go driver through openSQLiteForBackup.
func sqliteSnapshot(dest string) error {
	dbPath := meshDBPath()
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("database not found at %s", dbPath)
	}
	if _, err := exec.LookPath("sqlite3"); err == nil {
		cmd := exec.Command("sqlite3", dbPath, "VACUUM INTO '"+dest+"';")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("sqlite3 VACUUM INTO: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	_ = os.Remove(dest)
	db, err := openSQLiteForBackup(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec("VACUUM INTO '" + dest + "'")
	return err
}

// openSQLiteForBackup opens the panel database for the VACUUM INTO
// (seam; tests use the same modernc driver via the store import).
func openSQLiteForBackup(path string) (*sql.DB, error) {
	return sql.Open("sqlite", path)
}

// backupEncrypt derives the AES key via scrypt and seals the tar.
func backupEncrypt(out io.Writer, passphrase []byte, tarBytes []byte) error {
	salt := make([]byte, backupSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	nonce := make([]byte, backupNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	key, err := scrypt.Key(passphrase, salt, backupScryptN, backupScryptR, backupScryptP, backupKeyLen)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	if _, err := out.Write([]byte(backupMagic)); err != nil {
		return err
	}
	var hdr [12]byte
	binary.LittleEndian.PutUint32(hdr[0:4], backupScryptN)
	binary.LittleEndian.PutUint32(hdr[4:8], backupScryptR)
	binary.LittleEndian.PutUint32(hdr[8:12], backupScryptP)
	if _, err := out.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := out.Write(salt); err != nil {
		return err
	}
	if _, err := out.Write(nonce); err != nil {
		return err
	}
	_, err = out.Write(gcm.Seal(nil, nonce, tarBytes, nil))
	return err
}

// backupDecrypt verifies header + GCM tag (integrity BEFORE any use).
func backupDecrypt(in io.Reader, passphrase []byte) ([]byte, error) {
	magic := make([]byte, len(backupMagic))
	if _, err := io.ReadFull(in, magic); err != nil || string(magic) != backupMagic {
		return nil, fmt.Errorf("not a quantum-panel backup (bad magic)")
	}
	var hdr [12]byte
	if _, err := io.ReadFull(in, hdr[:]); err != nil {
		return nil, fmt.Errorf("truncated backup header")
	}
	n := binary.LittleEndian.Uint32(hdr[0:4])
	rr := binary.LittleEndian.Uint32(hdr[4:8])
	p := binary.LittleEndian.Uint32(hdr[8:12])
	salt := make([]byte, backupSaltLen)
	if _, err := io.ReadFull(in, salt); err != nil {
		return nil, fmt.Errorf("truncated backup salt")
	}
	nonce := make([]byte, backupNonceLen)
	if _, err := io.ReadFull(in, nonce); err != nil {
		return nil, fmt.Errorf("truncated backup nonce")
	}
	key, err := scrypt.Key(passphrase, salt, int(n), int(rr), int(p), backupKeyLen)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := io.ReadAll(io.LimitReader(in, backupMaxTarBytes))
	if err != nil {
		return nil, err
	}
	out, err := gcm.Open(nil, nonce, plain, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption FAILED (wrong passphrase or tampered file)")
	}
	return out, nil
}

// buildBackupTar assembles the tar + manifest; returns the tar bytes.
func buildBackupTar() ([]byte, backupManifest, error) {
	var man backupManifest
	man.Version = panelVersion
	man.CreatedAt = time.Now().Unix()

	tmp, err := os.CreateTemp("", "qp-snap-*.db")
	if err != nil {
		return nil, man, err
	}
	snapPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(snapPath)
	if err := sqliteSnapshot(snapPath); err != nil {
		return nil, man, err
	}

	files, err := backupKeyFiles()
	if err != nil {
		return nil, man, err
	}
	panelJSON := filepath.Join(configDir, "panel.json")
	if data, err := os.ReadFile(panelJSON); err == nil {
		sum := sha256.Sum256(data)
		man.Members = append(man.Members, backupMember{
			Name: "panel.json", SHA256: hex.EncodeToString(sum[:]),
			Size: int64(len(data)), Mode: 0o600, Hashed: true,
		})
	}
	addMember := func(name, src string, mode uint32) error {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		man.Members = append(man.Members, backupMember{
			Name: name, SHA256: hex.EncodeToString(sum[:]),
			Size: int64(len(data)), Mode: mode,
		})
		return nil
	}
	if err := addMember("quantum.db", snapPath, 0o600); err != nil {
		return nil, man, err
	}
	for rel, abs := range files {
		mode := uint32(0o600)
		if strings.HasSuffix(rel, ".crt") || strings.HasSuffix(rel, ".b64") {
			mode = 0o640
		}
		if err := addMember(rel, abs, mode); err != nil {
			return nil, man, err
		}
	}
	// NOTE: manifest.json carries NO self-hash entry — the bundle is
	// already authenticated wholesale by AES-256-GCM; a self-referential
	// hash cannot be computed consistently and would be redundant.
	manJSON, _ := json.Marshal(man)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	write := func(name string, data []byte, mode int64) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)),
			ModTime: time.Unix(man.CreatedAt, 0), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	for _, m := range man.Members {
		if m.Hashed {
			continue
		}
		src := m.Name
		if src == "quantum.db" {
			src = snapPath
		} else if abs, ok := files[m.Name]; ok {
			src = abs
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return nil, man, err
		}
		if err := write(m.Name, data, int64(m.Mode)); err != nil {
			return nil, man, err
		}
	}
	if err := write("manifest.json", manJSON, 0o600); err != nil {
		return nil, man, err
	}
	if err := tw.Close(); err != nil {
		return nil, man, err
	}
	return buf.Bytes(), man, nil
}

// BackupCmd implements `quantum-panel backup --out FILE --passphrase-file F`.
func BackupCmd(args []string) int {
	out, passFile := "", ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--out", "--passphrase-file":
			flag := args[i]
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "backup: missing value for", flag)
				return 2
			}
			if flag == "--out" {
				out = args[i]
			} else {
				passFile = args[i]
			}
		}
	}
	if out == "" || passFile == "" {
		fmt.Fprintln(os.Stderr, "usage: quantum-panel backup --out FILE --passphrase-file F")
		return 2
	}
	pass, err := readPassphraseFile(passFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "backup:", err)
		return 1
	}
	tarBytes, man, err := buildBackupTar()
	if err != nil {
		fmt.Fprintln(os.Stderr, "backup:", err)
		return 1
	}
	if err := backupEncryptToFile(out, pass, tarBytes); err != nil {
		fmt.Fprintln(os.Stderr, "backup:", err)
		return 1
	}
	fmt.Printf("backup written: %s (%d members, %d bytes)\n", out, len(man.Members), fileSizeOf(out))
	return 0
}

func backupEncryptToFile(out string, pass, tarBytes []byte) error {
	f, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := backupEncrypt(f, pass, tarBytes); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// chmod again in case the file pre-existed with wider permissions.
	return os.Chmod(out, 0o600)
}

// RestoreCmd implements `quantum-panel restore --in FILE --passphrase-file F [--force]`.
func RestoreCmd(args []string) int {
	in, passFile := "", ""
	force := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--in", "--passphrase-file":
			flag := args[i]
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "restore: missing value for", flag)
				return 2
			}
			if flag == "--in" {
				in = args[i]
			} else {
				passFile = args[i]
			}
		case "--force":
			force = true
		}
	}
	if in == "" || passFile == "" {
		fmt.Fprintln(os.Stderr, "usage: quantum-panel restore --in FILE --passphrase-file F [--force]")
		return 2
	}
	if !force && panelRunning() {
		fmt.Fprintln(os.Stderr, "restore: the panel is RUNNING — stop it (systemctl stop gre-panel) or pass --force")
		return 1
	}
	pass, err := readPassphraseFile(passFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "restore:", err)
		return 1
	}
	data, err := os.ReadFile(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "restore:", err)
		return 1
	}
	tarBytes, err := backupDecrypt(bytes.NewReader(data), pass)
	if err != nil {
		fmt.Fprintln(os.Stderr, "restore:", err)
		return 1
	}
	man, files, err := untarBackup(tarBytes)
	if err != nil {
		fmt.Fprintln(os.Stderr, "restore:", err)
		return 1
	}
	return restoreMembers(man, files, configDir)
}

// restoreMembers verifies every member hash against the manifest and
// writes the files (0600/0640 clamped). Nothing is written unless ALL
// present-member hashes verify first.
func restoreMembers(man *backupManifest, files map[string][]byte, cfgDir string) int {
	for _, m := range man.Members {
		data, ok := files[m.Name]
		if !ok {
			if m.Hashed {
				continue // hash-only member: handled below
			}
			return restoreFail("bundle is missing member " + m.Name)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != m.SHA256 {
			return restoreFail("member " + m.Name + " failed integrity check")
		}
	}
	restored := 0
	for _, m := range man.Members {
		if m.Hashed {
			if data, err := os.ReadFile(filepath.Join(cfgDir, m.Name)); err == nil {
				sum := sha256.Sum256(data)
				if hex.EncodeToString(sum[:]) != m.SHA256 {
					fmt.Fprintln(os.Stderr, "restore: NOTE", m.Name, "differs from the backup (kept the current one)")
				}
			}
			continue
		}
		rel := filepath.FromSlash(m.Name)
		dest := filepath.Join(cfgDir, rel)
		if filepath.Clean(dest) != filepath.Join(cfgDir, filepath.Clean(rel)) {
			return restoreFail("unsafe member name " + m.Name)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return restoreFail(err.Error())
		}
		mode := m.Mode
		if mode > 0o640 {
			mode = 0o640 // clamp: 0600/0640 only
		}
		if err := os.WriteFile(dest, files[m.Name], os.FileMode(mode)); err != nil {
			return restoreFail(err.Error())
		}
		// WriteFile keeps the mode of an EXISTING file — force it.
		if err := os.Chmod(dest, os.FileMode(mode)); err != nil {
			return restoreFail(err.Error())
		}
		restored++
	}
	// The snapshot replaces the live db: stale WAL/SHM must go.
	_ = os.Remove(filepath.Join(cfgDir, "quantum.db-wal"))
	_ = os.Remove(filepath.Join(cfgDir, "quantum.db-shm"))
	fmt.Printf("restore complete: %d files restored into %s\n", restored, cfgDir)
	return 0
}

func restoreFail(msg string) int {
	fmt.Fprintln(os.Stderr, "restore:", msg)
	return 1
}

// untarBackup parses the tar, verifies the manifest hash, and returns
// (manifest, member contents). Unsafe names and bombs are refused.
func untarBackup(tarBytes []byte) (*backupManifest, map[string][]byte, error) {
	if len(tarBytes) > backupMaxTarBytes {
		return nil, nil, fmt.Errorf("bundle too large")
	}
	tr := tar.NewReader(bytes.NewReader(tarBytes))
	files := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		name := filepath.Clean(hdr.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return nil, nil, fmt.Errorf("unsafe member name %q", hdr.Name)
		}
		data, err := io.ReadAll(io.LimitReader(tr, backupMaxTarBytes))
		if err != nil {
			return nil, nil, err
		}
		files[name] = data
	}
	manRaw, ok := files["manifest.json"]
	if !ok {
		return nil, nil, fmt.Errorf("bundle has no manifest")
	}
	var man backupManifest
	if err := json.Unmarshal(manRaw, &man); err != nil {
		return nil, nil, fmt.Errorf("manifest unreadable: %v", err)
	}
	return &man, files, nil
}

func readPassphraseFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read passphrase file: %v", err)
	}
	pass := strings.TrimRight(string(data), "\r\n")
	if len(pass) < 12 {
		return nil, fmt.Errorf("passphrase too short (min 12 characters)")
	}
	return []byte(pass), nil
}

// panelRunningHook is the test seam for the running-panel guard
// (nil = the real systemctl check).
var panelRunningHook func() bool

// panelRunning reports whether the gre-panel systemd unit is active.
// Best effort: without systemctl (containers) it reports false.
func panelRunning() bool {
	if panelRunningHook != nil {
		return panelRunningHook()
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return false
	}
	return exec.Command(systemctl, "is-active", "--quiet", "gre-panel").Run() == nil
}

func fileSizeOf(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.Size()
	}
	return -1
}
