package panel

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The UI script was extracted from index.html into app.js: the page must load
// it, the server must serve it, and it must not be left behind inline.
func TestAppJSExtracted(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), `<script src="app.js"></script>`) {
		t.Error("index.html must load app.js")
	}
	if !strings.Contains(string(html), `<script src="matrix_edit.js"></script>`) {
		t.Error("index.html must load the Matrix edit module")
	}
	if !strings.Contains(string(html), `<script src="topology.js"></script>`) {
		t.Error("index.html must load the topology module")
	}
	if !strings.Contains(string(html), `<script src="tunnel_overview.js"></script>`) {
		t.Error("page must load selected tunnel overview module")
	}
	// Five external modules (app, mesh tabs, Matrix edit, topology, selected
	// tunnel overview) plus the two inline theme/boot scripts.
	if n := strings.Count(string(html), "<script"); n != 7 {
		t.Errorf("unexpected number of <script> tags: %d", n)
	}
	if strings.Contains(string(html), "async function api(") {
		t.Error("UI code must live in app.js, not inline")
	}
}

func TestAppJSServedWithJSContentType(t *testing.T) {
	rr := httptest.NewRecorder()
	serveAsset("app.js", "text/javascript; charset=utf-8")(rr, httptest.NewRequest("GET", "/app.js", nil))
	if rr.Code != 200 || !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("%d %q", rr.Code, rr.Header().Get("Content-Type"))
	}
	if !strings.Contains(rr.Body.String(), "async function api(") {
		t.Error("app.js body missing the api() helper")
	}
}

func TestIndexCacheBustsAppJS(t *testing.T) {
	old := cfg
	defer func() { cfg = old }()
	cfg.BasePath = "x"
	rr := httptest.NewRecorder()
	serveIndex(rr, httptest.NewRequest("GET", "/x/", nil))
	if !strings.Contains(rr.Body.String(), `src="tunnel_overview.js?v=`) {
		t.Error("selected tunnel overview must be cache-busted")
	}
	if !strings.Contains(rr.Body.String(), `src="topology.js?v=`) {
		t.Error("topology module must be cache-busted per build")
	}
	if !strings.Contains(rr.Body.String(), `src="matrix_edit.js?v=`) {
		t.Error("Matrix edit module must be cache-busted per build")
	}
	if !strings.Contains(rr.Body.String(), `src="app.js?v=`) {
		t.Error("app.js must be cache-busted per build")
	}
}
