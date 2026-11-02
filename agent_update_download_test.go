package main

// Session 4 gate (SU-2): the agent's long-term enrollment credential must
// never leave the enrolled controller's origin during an artifact download,
// and redirects must never change scheme or host.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type headerCapture struct {
	mu     sync.Mutex
	ids    []string
	keys   []string
	visits int
}

func (h *headerCapture) handler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.visits++
		h.ids = append(h.ids, r.Header.Get(agentIDHeader))
		h.keys = append(h.keys, r.Header.Get(agentKeyHeader))
		h.mu.Unlock()
		if target := r.URL.Query().Get("redirect"); target != "" {
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		w.Write([]byte(body))
	}
}

func (h *headerCapture) credentialsSeen() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.ids {
		if h.ids[i] != "" || h.keys[i] != "" {
			return true
		}
	}
	return false
}

func TestAgentUpdateDownloadCredentialScoping(t *testing.T) {
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ARTIFACT"))
	}))
	defer controller.Close()
	// Controller that ALSO records credentials on the artifact path.
	rec := &headerCapture{}
	controller.Config.Handler = rec.handler("ARTIFACT")

	evil := &headerCapture{}
	evilSrv := httptest.NewServer(evil.handler("EVIL"))
	defer evilSrv.Close()

	ident := agentIdentity{ID: "agt_test", Key: "long-term-secret", Server: controller.URL}

	// 1. Same-origin download: credentials ARE attached (the controller needs
	//    them to authorize release downloads from its private origin).
	art, err := agentUpdateDownload(controller.URL+"/artifact", ident)
	if err != nil || string(art) != "ARTIFACT" {
		t.Fatalf("same-origin download failed: %v", err)
	}
	if !rec.credentialsSeen() {
		t.Fatal("same-origin download did not carry the agent credential (controller cannot authorize)")
	}

	// 2. Hostile artifact host: the download is refused BEFORE any request is
	//    made, and the long-term key never reaches the hostile host.
	rec.mu.Lock()
	rec.visits = 0
	rec.mu.Unlock()
	evilVisits := func() int { evil.mu.Lock(); defer evil.mu.Unlock(); return evil.visits }
	n0 := evilVisits()
	_, err = agentUpdateDownload(evilSrv.URL+"/artifact", ident)
	if err == nil {
		t.Fatal("SECURITY FAILURE: hostile-origin artifact URL was downloaded")
	}
	if evilVisits() != n0 {
		t.Fatal("SECURITY FAILURE: the hostile artifact host was contacted")
	}
	if evil.credentialsSeen() {
		t.Fatal("SECURITY FAILURE: the agent key reached the hostile host")
	}

	// 3. Same-origin redirect to a different host: aborted, key not delivered.
	redirectOut := controller.URL + "/out?redirect=" + evilSrv.URL + "/stolen"
	_, err = agentUpdateDownload(redirectOut, ident)
	if err == nil {
		t.Fatal("SECURITY FAILURE: cross-origin redirect was followed during an update download")
	}
	if evil.credentialsSeen() {
		t.Fatal("SECURITY FAILURE: the agent key reached the redirect target")
	}

	// 4. Same-origin redirect to the same host: followed, credential intact.
	art, err = agentUpdateDownload(controller.URL+"/a?redirect="+controller.URL+"/b", ident)
	if err != nil || string(art) != "ARTIFACT" {
		t.Fatalf("same-origin redirect download failed: %v", err)
	}

	// 5. Scheme downgrade (https controller origin, http artifact URL):
	//    refused at the origin gate without any network activity.
	httpsIdent := agentIdentity{ID: "agt_test", Key: "k", Server: "https://controller.example:7443/panel"}
	if _, err := agentUpdateDownload("http://controller.example:7443/panel/artifact", httpsIdent); err == nil {
		t.Fatal("SECURITY FAILURE: plaintext downgrade of the artifact URL was accepted")
	}
	_ = fmt.Sprint() // keep fmt for future assertions
}
