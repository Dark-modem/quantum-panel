package panel

// uierrors.go — the ONE structured error catalog for the new UI API
// (Phase 11, requirement 6). Every error the new endpoints emit carries
// BOTH fa and en texts: {title, what_happened, likely_cause,
// what_to_do} + redacted details + a docs_ref, with the HTTP status and
// the legacy flat fields (error_code/error/hint) preserved so old
// consumers keep working.
//
// Redaction is structural: handlers may only pass identifiers, names,
// counts and short reason strings into details; free-text reasons from
// agents go through redactErrorText, which strips anything that looks
// like a credential. TestUIErrorCatalog enforces fa+en completeness and
// TestUIErrorRedaction enforces the scrubbing.

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

type uiErrText struct {
	en string
	fa string
}

type uiErrEntry struct {
	status   int
	title    uiErrText
	happened uiErrText
	cause    uiErrText
	todo     uiErrText
}

var uiErrCatalog = map[string]uiErrEntry{
	"E-AUTH-01": {401,
		uiErrText{"Not signed in", "وارد نشده‌اید"},
		uiErrText{"The request arrived without a valid session.", "درخواست بدون نشست معتبر رسید."},
		uiErrText{"The session expired or the cookie was dropped.", "نشست منقضی شده یا کوکی از بین رفته است."},
		uiErrText{"Sign in again from the login screen.", "دوباره از صفحه ورود وارد شوید."}},
	"E-AUTH-08": {403,
		uiErrText{"Security token mismatch", "عدم تطابق توکن امنیتی"},
		uiErrText{"The request lacked the anti-CSRF token.", "درخواست توکن ضد جعل (CSRF) نداشت."},
		uiErrText{"The page was stale or the call skipped the token header.", "صفحه قدیمی بوده یا توکن ارسال نشده است."},
		uiErrText{"Reload the page and try again.", "صفحه را دوباره بارگذاری کنید."}},
	"E-NOTFOUND": {404,
		uiErrText{"Not found", "یافت نشد"},
		uiErrText{"The requested object does not exist (or no longer does).", "شیء درخواستی وجود ندارد."},
		uiErrText{"It was deleted, or the list is stale.", "حذف شده یا فهرست قدیمی است."},
		uiErrText{"Refresh the list and retry.", "فهرست را تازه کنید و دوباره تلاش کنید."}},
	"E-VALIDATION": {400,
		uiErrText{"Invalid input", "ورودی نامعتبر"},
		uiErrText{"The request payload failed validation.", "بدنه درخواست اعتبارسنجی را رد کرد."},
		uiErrText{"A field is missing or out of range (see details).", "فیلدی کم است یا خارج از محدوده است (در details)."},
		uiErrText{"Fix the field named in details and resubmit.", "فیلد مشخص‌شده در details را اصلاح و دوباره ارسال کنید."}},
	"E-AGENT-OFFLINE": {409,
		uiErrText{"Agent offline", "عامل آفلاین است"},
		uiErrText{"The agent is not connected to the panel right now.", "عامل در حال حاضر به پنل متصل نیست."},
		uiErrText{"The agent service is stopped or the server lost its connection.", "سرویس عامل متوقف است یا ارتباط قطع شده است."},
		uiErrText{"Check the agent service on the server (systemctl status quantum-agent), then retry.", "وضعیت سرویس quantum-agent روی سرور را بررسی و دوباره تلاش کنید."}},
	"E-AGENT-NO-SERVER": {409,
		uiErrText{"Agent not linked to a server", "عامل به سرور متصل نیست"},
		uiErrText{"The agent has no server record, so its role and tunnels cannot resolve.", "عامل هیچ رکورد سروری ندارد؛ نقش و تونل‌ها قابل تعیین نیستند."},
		uiErrText{"Enrollment finished but the server link step was skipped.", "ثبت‌نام انجام شده اما مرحله اتصال به سرور طی نشده است."},
		uiErrText{"Open Agents > this agent > Link and connect it to a server.", "از بخش Agents این عامل را به یک سرور متصل کنید."}},
	"E-ENGINE-NOT-ENABLED": {409,
		uiErrText{"Engine not enabled", "موتور فعال نیست"},
		uiErrText{"The tunnel's engine is not enabled on one of the two agents.", "موتور تونل روی یکی از دو عامل فعال نشده است."},
		uiErrText{"The engine was never enabled on that agent, or it was disabled earlier.", "موتور روی آن عامل فعال نشده یا قبلاً غیرفعال شده است."},
		uiErrText{"Open Agents > the named agent > Engines and enable the engine, then retry.", "Agents > عامل موردنظر > Engines را باز کنید، موتور را فعال و دوباره تلاش کنید."}},
	"E-ENGINE-UNKNOWN": {400,
		uiErrText{"Unknown engine", "موتور ناشناخته"},
		uiErrText{"The engine name is not in this panel's catalog.", "نام موتور در فهرست این پنل نیست."},
		uiErrText{"A typo, or the agent is older than the engine.", "غلط تایپی یا نسخه عامل قدیمی‌تر از موتور است."},
		uiErrText{"Check the engine name (Engines tab); upgrade the agent if the engine is new.", "نام موتور را در برگه Engines بررسی کنید؛ در صورت جدید بودن، عامل را ارتقا دهید."}},
	"E-ENGINE-NO-ARTIFACT": {409,
		uiErrText{"No artifact for this architecture", "مصرفی برای این معماری نیست"},
		uiErrText{"No signed engine artifact exists for the agent's architecture, and this engine needs a binary.", "برای معماری این عامل مصرف امضاشده‌ای وجود ندارد و این موتور به فایل باینری نیاز دارد."},
		uiErrText{"The artifact was never uploaded for this arch (or it was deleted).", "مصرف برای این معماری بارگذاری نشده یا حذف شده است."},
		uiErrText{"Upload the signed artifact in Engines > Artifacts for this arch, or use a kernel engine.", "در Engines > Artifacts مصرف امضاشده این معماری را بارگذاری کنید یا از موتور هسته‌ای استفاده کنید."}},
	"E-ENGINE-INSTALL-FAILED": {502,
		uiErrText{"Engine install failed on the agent", "نصب موتور روی عامل شکست خورد"},
		uiErrText{"The agent could not download or verify the engine binary.", "عامل نتوانست باینری موتور را دریافت یا تأیید کند."},
		uiErrText{"The artifact is missing/corrupt, the panel is unreachable from the agent, or the signature does not match.", "مصرف ناقص یا خراب است، عامل به پنل دسترسی ندارد یا امضا مطابقت ندارد."},
		uiErrText{"Check the agent's connectivity to the panel and re-upload the artifact; see the agent log for the reason.", "اتصال عامل به پنل را بررسی، مصرف را دوباره بارگذاری کنید؛ علت در لاگ عامل است."}},
	"E-ENGINE-START-FAILED": {502,
		uiErrText{"Tunnel start failed on the agent", "اجرای تونل روی عامل شکست خورد"},
		uiErrText{"The agent could not start the tunnel process.", "عامل نتوانست فرایند تونل را اجرا کند."},
		uiErrText{"The exact agent reason is in details.raw (often: port in use, bad parameter, missing permission).", "علت دقیق در details.raw است (اغلب: اشغال بودن پورت، پارامتر اشتباه، نبود مجوز)."},
		uiErrText{"Fix the named cause (e.g. pick another port) and push the tunnel again.", "علت را برطرف کنید (مثلاً پورت دیگری انتخاب کنید) و تونل را دوباره اعمال کنید."}},
	"E-ENGINE-PORT-CONFLICT": {409,
		uiErrText{"Port already in use", "پورت اشغال است"},
		uiErrText{"A port this tunnel needs is already taken on the agent.", "پورت موردنیاز تونل روی عامل اشغال است."},
		uiErrText{"Another service (or another tunnel) already binds it.", "سرویس یا تونل دیگری آن را اشغال کرده است."},
		uiErrText{"Pick a different port for the tunnel and retry.", "پورت دیگری برای تونل انتخاب و دوباره تلاش کنید."}},
	"E-ENGINE-SIGNATURE": {502,
		uiErrText{"Artifact signature refused", "امضای مصرف رد شد"},
		uiErrText{"The agent refused the engine artifact: sha256 or ed25519 signature mismatch.", "عامل مصرف را رد کرد: عدم تطابق sha256 یا امضای ed25519."},
		uiErrText{"The artifact was modified, corrupted, or signed by a key the agent does not pin.", "مصرف تغییر یافته، خراب شده، یا با کلیدی امضا شده که عامل نمی‌شناسد."},
		uiErrText{"Re-upload a genuine signed artifact for this engine and arch.", "مصرف اصلی امضاشده را برای این موتور و معماری دوباره بارگذاری کنید."}},
	"E-ENGINE-KEY-MISMATCH": {502,
		uiErrText{"Engine key mismatch", "ناسازگاری کلید موتور"},
		uiErrText{"The two tunnel ends do not share the expected key or PSK.", "دو سر تونل کلید یا PSK موردانتظار را ندارند."},
		uiErrText{"Keys were generated separately or one end was re-keyed.", "کلیدها جداگانه ساخته شده یا یک سر تعویض شده است."},
		uiErrText{"Re-run key generation for the tunnel so both ends share the same material.", "تولید کلید تونل را تکرار کنید تا دو سر هم‌خوان شوند."}},
	"E-ENGINE-PEER-UNREACH": {502,
		uiErrText{"Peer unreachable", "همتا در دسترس نیست"},
		uiErrText{"The tunnel end cannot reach its peer server.", "یک سر تونل نمی‌تواند به سرور همتا برسد."},
		uiErrText{"Wrong address, firewall, or the remote end is down.", "آدرس اشتباه، فایروال، یا سر راهتی خاموش است."},
		uiErrText{"Verify the remote address/port and that the peer service is running.", "آدرس/پورت مقابل و روشن بودن سرویس همتا را بررسی کنید."}},
	"E-ENGINE-PERMISSION": {502,
		uiErrText{"Permission denied on the agent", "مجوز روی عامل رد شد"},
		uiErrText{"The agent lacks OS permission for this engine operation.", "عامل مجوز سیستمی برای این کار را ندارد."},
		uiErrText{"Missing capability, root, or the binary is not executable.", "نبود قابلیت (capability)، دسترسی root، یا اجرایی نبودن فایل."},
		uiErrText{"Run the installer's hardening step on the agent host, then retry.", "مرحله hardening نصب‌کننده را روی میزبان عامل اجرا و دوباره تلاش کنید."}},
	"E-ENGINE-KERNEL-MODULE": {502,
		uiErrText{"Kernel module missing", "ماژول هسته نیست"},
		uiErrText{"The engine needs a kernel module the host does not have.", "موتور به ماژول هسته‌ای نیاز دارد که میزبان ندارد."},
		uiErrText{"The module is not loaded/available on this kernel (see details.module).", "ماژول روی این هسته بارگذاری یا موجود نیست (در details.module)."},
		uiErrText{"Load/install the named module (modprobe) or choose an engine without kernel requirements.", "ماژول را بارگذاری کنید (modprobe) یا موتوری بدون نیاز به هسته انتخاب کنید."}},
	"E-ENGINE-UNKNOWN-ERROR": {502,
		uiErrText{"Agent-reported error", "خطای گزارش‌شده عامل"},
		uiErrText{"The agent reported an error the panel has no translation for.", "عامل خطایی گزارش کرده که پنل ترجمه‌ای برایش ندارد."},
		uiErrText{"Anything outside the common catalog.", "هر چیزی خارج از فهرست رایج."},
		uiErrText{"Read the raw text in details.raw; escalate to support with the agent diag bundle if it repeats.", "متن خام در details.raw؛ در صورت تکرار با بسته diag عامل به پشتیبانی منتقل کنید."}},
	"E-WEIGHT-CONFLICT": {409,
		uiErrText{"Weight target mismatch", "ناسازگاری محدوده وزن"},
		uiErrText{"The weight write would touch tunnels of another topology.", "نوشتن وزن به تونل‌های توپولوژی دیگری برخورد می‌کند."},
		uiErrText{"The tunnel belongs to a different topology than the request path.", "تونل به توپولوژی دیگری تعلق دارد."},
		uiErrText{"Use the tunnel's own topology endpoint.", "از نشانی توپولوژی خود تونل استفاده کنید."}},
	"E-CONFIRM-REQUIRED": {409,
		uiErrText{"Confirmation required", "نیاز به تأیید دارد"},
		uiErrText{"The destructive call arrived without its confirmation token (or the token expired/was used).", "فرایند مخرب بدون توکن تأیید (یا با توکن منقضی/مصرف‌شده) رسید."},
		uiErrText{"The resource has dependants; the token proves the dependency report was seen.", "این منبع وابسته دارد؛ توکن نشان می‌دهد گزارش وابستگی دیده شده است."},
		uiErrText{"GET the resource's /dependencies report, read the dependant list, and resend with the X-Confirm-Token header.", "گزارش /dependencies آن منبع را بگیرید، فهرست وابستگی‌ها را بخوانید و با هدر X-Confirm-Token دوباره ارسال کنید."}},
	"E-STORE": {500,
		uiErrText{"Storage error", "خطای ذخیره‌سازی"},
		uiErrText{"The panel's database operation failed.", "عملیات پایگاه‌داده پنل شکست خورد."},
		uiErrText{"Disk full/permission or a transient SQLite lock.", "پر بودن دیسک، مجوز، یا قفل موقت SQLite."},
		uiErrText{"Retry; if it repeats check the panel host disk and logs.", "دوباره تلاش کنید؛ در تکرار، دیسک و لاگ میزبان پنل را بررسی کنید."}},
	"E-RATE": {429,
		uiErrText{"Too many requests", "درخواست بیش از حد"},
		uiErrText{"The endpoint was called too often.", "این نشانی بیش از حد فراخوانده شده است."},
		uiErrText{"Polling faster than the documented cadence.", "است polls سریع‌تر از فواصل توصیه‌شده."},
		uiErrText{"Wait the seconds given in details.retry_after_s.", "به اندازه details.retry_after_s ثانیه صبر کنید."}},
}

// uiDetailSanitizer: allowed detail KEYS (everything else is dropped —
// a typo must never leak a secret field).
var uiDetailAllowed = map[string]bool{
	"agent": true, "agent_name": true, "engine": true, "tunnel": true,
	"tunnels": true, "topology": true, "server": true, "port": true,
	"alternatives": true,
	"arch":         true, "module": true, "op": true, "field": true,
	"retry_after_s": true, "raw": true, "reason": true, "role": true,
}

var uiSecretLike = regexp.MustCompile(`(?i)(key|token|secret|password|passwd|psk|auth|cookie|credential|pin\b)`)

// redactErrorText scrubs an agent-provided free text: secret-shaped
// assignments (key=..., token: ...) are masked; long runs are capped.
func redactErrorText(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	s = uiSecretLike.ReplaceAllStringFunc(s, func(word string) string {
		// keep the word itself when it is just an error-class keyword
		lower := strings.ToLower(word)
		switch lower {
		case "key", "keys", "unauthorized", "permission", "authorize":
			return word
		}
		return "[redacted]"
	})
	for _, pat := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(key|token|secret|psk|password|passwd)\s*[:=]\s*\S+`),
		regexp.MustCompile(`(?i)\b(AAAAC[0-9A-Za-z+/=_-]{16,})`), // openssh-ed25519 private prefix
		regexp.MustCompile(`\b[0-9a-f]{64}\b`),                   // bare sha/hex key material
	} {
		s = pat.ReplaceAllString(s, "$1=[redacted]")
	}
	return s
}

// uiSanitizeDetails filters keys through the allowlist and scrubs every
// string value.
func uiSanitizeDetails(details map[string]any) map[string]any {
	out := map[string]any{}
	keys := make([]string, 0, len(details))
	for k := range details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !uiDetailAllowed[k] {
			continue // dropped: typos must never leak secret fields
		}
		v := details[k]
		if sv, ok := v.(string); ok {
			v = redactErrorText(sv)
		}
		out[k] = v
	}
	return out
}

// uiErrWrite emits the full structured error envelope. `raw` is an
// optional agent-provided reason string (redacted before inclusion).
func uiErrWrite(w http.ResponseWriter, r *http.Request, code string, details map[string]any, raw string) {
	e, ok := uiErrCatalog[code]
	if !ok {
		code = "E-ENGINE-UNKNOWN-ERROR"
		e = uiErrCatalog[code]
	}
	if raw != "" {
		if details == nil {
			details = map[string]any{}
		}
		if _, has := details["raw"]; !has {
			details["raw"] = raw
		}
	}
	resp := map[string]any{
		"error_code":  code,
		"error":       e.title.en,
		"hint":        e.todo.en,
		"http_status": e.status,
		"ui": map[string]any{
			"code":          code,
			"title":         map[string]string{"en": e.title.en, "fa": e.title.fa},
			"what_happened": map[string]string{"en": e.happened.en, "fa": e.happened.fa},
			"likely_cause":  map[string]string{"en": e.cause.en, "fa": e.cause.fa},
			"what_to_do":    map[string]string{"en": e.todo.en, "fa": e.todo.fa},
			"details":       uiSanitizeDetails(details),
			"docs_ref":      "00-ADMIN/UI-API-CONTRACT.md#1-error-format",
		},
	}
	writeJSONStatus(w, e.status, resp)
}

// writeJSONStatus is writeJSON with an explicit status code.
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// classifyAgentError maps an agent-reported error text to a catalog
// code ("" = no mapping; the caller uses E-ENGINE-UNKNOWN-ERROR and
// keeps the raw text). Ordered: most specific first.
func classifyAgentError(text string) string {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "address already in use"), strings.Contains(t, "bind: port"), strings.Contains(t, "port is already"), strings.Contains(t, "port already"):
		return "E-ENGINE-PORT-CONFLICT"
	case strings.Contains(t, "signature"), strings.Contains(t, "sha256"), strings.Contains(t, "verify artifact"), strings.Contains(t, "ed25519"):
		return "E-ENGINE-SIGNATURE"
	case strings.Contains(t, "no such file or directory"), strings.Contains(t, "binary not found"), strings.Contains(t, "executable file not found"), strings.Contains(t, "artifact missing"), strings.Contains(t, "no artifact"):
		return "E-ENGINE-INSTALL-FAILED"
	case strings.Contains(t, "key mismatch"), strings.Contains(t, "psk mismatch"), strings.Contains(t, "handshake failed"), strings.Contains(t, "bad key"), strings.Contains(t, "key is incorrect"):
		return "E-ENGINE-KEY-MISMATCH"
	case strings.Contains(t, "connection refused"), strings.Contains(t, "no route to host"), strings.Contains(t, "i/o timeout"), strings.Contains(t, "unreachable"), strings.Contains(t, "dial tcp"):
		return "E-ENGINE-PEER-UNREACH"
	case strings.Contains(t, "permission denied"), strings.Contains(t, "operation not permitted"), strings.Contains(t, "access denied"), strings.Contains(t, "could not set capability"):
		return "E-ENGINE-PERMISSION"
	case strings.Contains(t, "module"), strings.Contains(t, "not permitted in this kernel") && strings.Contains(t, "wireguard"):
		return "E-ENGINE-KERNEL-MODULE"
	case strings.Contains(t, "unknown engine"), strings.Contains(t, "not registered"):
		return "E-ENGINE-UNKNOWN"
	default:
		return ""
	}
}
