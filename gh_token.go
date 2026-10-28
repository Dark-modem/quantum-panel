package main

// GitHub access token for the private update repository, entered from the
// panel's Update tab. Stored 0600 at <configDir>/github-token; githubToken()
// (update_channel.go) reads it fresh on every call, so saving takes effect
// without a restart. The token never leaves this server except to github.com.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func githubTokenPath() string { return filepath.Join(configDir, "github-token") }

// handleGithubTokenGet reports whether a token is stored — never its value.
func handleGithubTokenGet(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(githubTokenPath())
	writeJSON(w, map[string]bool{"set": err == nil && strings.TrimSpace(string(data)) != ""})
}

// handleGithubTokenPost stores the token (0600); an empty token removes it.
func handleGithubTokenPost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeAPIError(w, r, "E-ACTION-01", "invalid json")
		return
	}
	tok := strings.TrimSpace(req.Token)
	if tok != "" && len(tok) < 20 {
		writeAPIError(w, r, "E-UPDATE-08", "that does not look like a GitHub token (want github_pat_… or ghp_…)")
		return
	}
	if err := os.MkdirAll(configDir, 0700); err != nil {
		writeAPIError(w, r, "E-UPDATE-04", err.Error())
		return
	}
	var detail string
	if tok == "" {
		detail = "token removed"
		if err := os.Remove(githubTokenPath()); err != nil && !os.IsNotExist(err) {
			writeAPIError(w, r, "E-UPDATE-04", err.Error())
			return
		}
	} else {
		detail = "token saved — update checks use it from now on"
		if err := os.WriteFile(githubTokenPath(), []byte(tok+"\n"), 0600); err != nil {
			writeAPIError(w, r, "E-UPDATE-04", err.Error())
			return
		}
	}
	dropReleaseCache()
	LogSecurityAudit("github_token", cfg.Username, ClientIP(r), detail)
	writeJSON(w, map[string]string{"status": "ok", "detail": detail})
}
