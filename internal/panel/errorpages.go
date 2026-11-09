package panel

// errorPages.go — branded HTML error pages for browser page requests.
//
// The panel is a JSON API with one HTML app, so an error page must never leak
// into an API response: the middleware below replaces the body ONLY when the
// request looks like a browser page navigation (Accept advertises text/html
// AND the path is not an API path). API errors keep their exact JSON bodies
// and status codes. The page itself is fully self-contained (inline CSS, no
// external assets, no request details) so it renders under any base path and
// leaks nothing about the server.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

// errorPageCopy is the Persian title + one-sentence explanation for each
// handled status. Codes missing here fall through to the generic page.
var errorPageCopy = map[int][2]string{
	http.StatusBadRequest:            {"درخواست نامعتبر", "درخواستی که فرستاده شد شکل درستی نداشت؛ صفحه را دوباره بارگذاری کنید."},
	http.StatusUnauthorized:          {"ورود لازم است", "برای دیدن این صفحه باید اول وارد حساب خود شوید."},
	http.StatusForbidden:             {"دسترسی مجاز نیست", "اجازه‌ی دیدن این صفحه را ندارید؛ با مدیر سرور تماس بگیرید."},
	http.StatusNotFound:              {"صفحه پیدا نشد", "آدرسی که باز کردید وجود ندارد یا جابه‌جا شده است."},
	http.StatusMethodNotAllowed:      {"روش درخواست پشتیبانی نمی‌شود", "این آدرس با این روش (GET/POST/…) پاسخ نمی‌دهد."},
	http.StatusRequestTimeout:        {"زمان درخواست به پایان رسید", "سرور دیر پاسخ داد؛ صفحه را دوباره بارگذاری کنید."},
	http.StatusConflict:              {"تضاد در انجام درخواست", "درخواست با وضعیت فعلی سرور سازگار نیست؛ صفحه را تازه کنید و دوباره تلاش کنید."},
	http.StatusRequestEntityTooLarge: {"حجم درخواست بیش از حد مجاز است", "داده‌ای که فرستادید بزرگ‌تر از حد مجاز است."},
	http.StatusUnprocessableEntity:   {"امکان پردازش درخواست نیست", "محتوای درخواست درست خوانده شد ولی قابل انجام نبود."},
	http.StatusTooManyRequests:       {"تعداد درخواست‌ها بیش از حد مجاز است", "چند لحظه صبر کنید و بعد دوباره تلاش کنید."},
	http.StatusInternalServerError:   {"خطای داخلی سرور", "مشکلی در سرور پیش آمد؛ این خطا ثبت شد و چند لحظه بعد دوباره تلاش کنید."},
	http.StatusBadGateway:            {"سرویس در دسترس نیست", "سرویس در دسترس نیست؛ چند لحظه بعد دوباره تلاش کنید."},
	http.StatusServiceUnavailable:    {"سرویس در دسترس نیست", "سرویس در دسترس نیست؛ چند لحظه بعد دوباره تلاش کنید."},
	http.StatusGatewayTimeout:        {"سرویس در دسترس نیست", "سرویس در دسترس نیست؛ چند لحظه بعد دوباره تلاش کنید."},
}

// errorPageRef builds the cosmetic reference ID shown on the page (QR-XXXXXXXX)
// and sent as X-Request-Id. Random per error — enough to correlate a support
// conversation with server logs, meaningless to an attacker.
func errorPageRef() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "QR-00000000"
	}
	return "QR-" + hex.EncodeToString(b[:])
}

// errorPagesWanted reports whether this request is a browser page navigation:
// it advertises text/html in Accept and is not an API path. Anything else
// (curl, the agents' WS, fetch() calls) gets the raw upstream response.
func errorPagesWanted(r *http.Request) bool {
	if r == nil || strings.Contains(r.URL.Path, "/api/") {
		return false
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html")
}

// errorPagesMiddleware renders the branded Persian error page for browser
// page requests that end in an error status. Everything else passes through
// byte-for-byte, so API errors always stay JSON.
func errorPagesMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// WebSocket upgrades (agent hub) hijack the connection: never
		// interpose a wrapper between them and the net/http machinery.
		if isWebSocketUpgrade(r) || !errorPagesWanted(r) {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&errorPageWriter{ResponseWriter: w}, r)
	})
}

// errorPageWriter intercepts the status and, on a browser-facing error,
// swaps the body for the branded page while keeping the status code.
type errorPageWriter struct {
	http.ResponseWriter
	status  int  // status seen so far (0 = none yet)
	pageOut bool // the error page was written; discard the handler's body
}

func (ew *errorPageWriter) WriteHeader(code int) {
	if ew.status != 0 {
		return // superfluous second WriteHeader: ignore like net/http does
	}
	ew.status = code
	if code >= 400 {
		ew.pageOut = true
		ew.writeErrorPage(code)
		return
	}
	ew.ResponseWriter.WriteHeader(code)
}

func (ew *errorPageWriter) Write(p []byte) (int, error) {
	if ew.status == 0 {
		// Body before an explicit status means the handler already chose to
		// stream (implicit 200): pass through untouched, never intercept.
		ew.status = http.StatusOK
		ew.ResponseWriter.WriteHeader(http.StatusOK)
	}
	if ew.pageOut {
		return len(p), nil // absorb the original error body; the page replaces it
	}
	return ew.ResponseWriter.Write(p)
}

func (ew *errorPageWriter) Flush() {
	if f, ok := ew.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// writeErrorPage renders the branded page. Headers still unset: safe to set
// Content-Type before WriteHeader here.
func (ew *errorPageWriter) writeErrorPage(code int) {
	ref := errorPageRef()
	h := ew.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Request-Id", ref)
	ew.ResponseWriter.WriteHeader(code)
	_, _ = ew.ResponseWriter.Write([]byte(errorPageHTML(code, ref)))
}

// errorPageHTML builds the self-contained Persian error page: dark Quantum
// branding (the tokens.css palette), dir=rtl, no external assets, no request
// details. Links are built from the configured base path so they land on the
// real pages no matter which path triggered the error.
func errorPageHTML(code int, ref string) string {
	meshHref := "/mesh"
	homeHref := "/"
	if bp := strings.Trim(cfg.BasePath, "/"); bp != "" {
		meshHref = "/" + bp + "/mesh"
		homeHref = "/" + bp + "/"
	}
	copyPair, known := errorPageCopy[code]
	title, expl := copyPair[0], copyPair[1]
	if !known {
		title = "خطا " + fmt.Sprint(code)
		expl = "درخواست انجام نشد. چند لحظه بعد دوباره تلاش کنید."
	}
	loginHref := strings.TrimSuffix(meshHref, "/mesh") + "/api/login"
	return `<!DOCTYPE html>
<html dir="rtl" lang="fa">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + title + ` · Quantum</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  html { -webkit-text-size-adjust: 100%; }
  body {
    min-height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 24px;
    background: #09090b;
    color: #f4f4f5;
    font-family: Vazirmatn, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    line-height: 1.7;
    text-align: right;
  }
  .card {
    width: 100%;
    max-width: 460px;
    background: #121214;
    border: 1px solid rgba(255, 255, 255, 0.07);
    border-radius: 16px;
    padding: 40px 32px 32px;
    text-align: center;
    box-shadow: 0 4px 20px rgba(0, 0, 0, 0.5);
  }
  .glyph {
    width: 52px; height: 52px;
    margin: 0 auto 20px;
    border-radius: 14px;
    background: rgba(56, 189, 248, 0.12);
    border: 1px solid rgba(56, 189, 248, 0.3);
    display: flex; align-items: center; justify-content: center;
  }
  .glyph svg { width: 28px; height: 28px; stroke: #38bdf8; fill: none; stroke-width: 1.9; stroke-linecap: round; stroke-linejoin: round; }
  h1 { font-size: 1.375rem; font-weight: 700; margin-bottom: 8px; }
  .code {
    display: inline-block;
    font-size: 0.75rem;
    font-weight: 600;
    letter-spacing: 0.08em;
    color: #a1a1aa;
    background: rgba(255, 255, 255, 0.06);
    border: 1px solid rgba(255, 255, 255, 0.07);
    border-radius: 999px;
    padding: 2px 12px;
    margin-bottom: 16px;
  }
  p.desc { font-size: 0.875rem; color: #a1a1aa; margin-bottom: 24px; }
  .home {
    display: inline-block;
    font-size: 0.875rem;
    font-weight: 600;
    color: #071018;
    background: #38bdf8;
    border-radius: 10px;
    padding: 10px 22px;
    text-decoration: none;
    transition: background 0.15s ease;
  }
  .home:hover, .home:focus-visible { background: #7dd3fc; outline: none; }
  .ref { margin-top: 24px; font-size: 0.75rem; color: #8e8e98; direction: ltr; unicode-bidi: embed; }
  .lform { display: none; flex-direction: column; gap: 10px; margin-bottom: 20px; }
  .lform.open { display: flex; }
  .lform input {
    width: 100%;
    font: inherit;
    color: #f4f4f5;
    background: #09090b;
    border: 1px solid rgba(255,255,255,0.12);
    border-radius: 10px;
    padding: 10px 12px;
    text-align: right;
  }
  .lform input:focus { outline: 2px solid #38bdf8; outline-offset: 1px; }
  .lerr { font-size: 0.8125rem; color: #f87171; min-height: 1.2em; }
  .alt { display: block; margin-top: 12px; font-size: 0.8125rem; color: #38bdf8; text-decoration: none; }
  .alt:hover { text-decoration: underline; }
  @media (max-width: 480px) {
    body { padding: 16px; align-items: flex-start; }
    .card { padding: 32px 20px 24px; border-radius: 12px; }
    h1 { font-size: 1.1875rem; }
  }
</style>
</head>
<body>
  <main class="card" role="alert">
    <div class="glyph" aria-hidden="true">
      <svg viewBox="0 0 24 24"><path d="M12 2l7 4v6c0 4-3 7-7 10-4-3-7-6-7-10V6z"/><path d="M12 8v4"/><path d="M12 15.5v.5"/></svg>
    </div>
    <h1>` + title + `</h1>
    <span class="code">` + fmt.Sprint(code) + `</span>
    <p class="desc">` + expl + `</p>` + func() string {
		if code == http.StatusUnauthorized {
			return `<form class="lform open" id="lform">
      <input id="l-user" autocomplete="username" placeholder="نام کاربری">
      <input id="l-pass" type="password" autocomplete="current-password" placeholder="گذرواژه">
      <div class="lerr" id="l-err"></div>
      <button class="home" type="submit">ورود</button>
    </form>
    <a class="alt" href="` + meshHref + `">پنل کوانتوم</a>
    <script>
    document.getElementById('lform').addEventListener('submit', function (ev) {
      ev.preventDefault();
      var u = document.getElementById('l-user').value;
      var p = document.getElementById('l-pass').value;
      fetch('` + loginHref + `', { method: 'POST', credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: u, password: p }) })
        .then(function (r) {
          return r.json().catch(function () { return {}; }).then(function (j) {
            if (!r.ok) throw new Error(j.hint || j.error || 'نام کاربری یا گذرواژه نادرست است');
            location.replace('` + meshHref + `');
          });
        })
        .catch(function (e) { document.getElementById('l-err').textContent = e.message; });
    });
    </script>`
		}
		return `<a class="home" href="` + homeHref + `">بازگشت به داشبورد</a>
    <a class="alt" href="` + meshHref + `">پنل کوانتوم</a>`
	}() + `
    <div class="ref">` + ref + `</div>
  </main>
</body>
</html>
`
}
