# PANEL-LISTENER-NOTES — why the panel must never move its own ports

Phase 6 fix for session 1's report: in the netns e2e the panel's listener
REBOUND mid-run and agents got "connection refused" at enroll.

## Root cause (was in main.go + tls.go)

- `ensureFreeHTTPPort()` ran on EVERY start. If the saved port was busy —
  a TIME_WAIT window, a short-lived test process, a parallel e2e panel —
  it scanned upward, **wrote the new port into panel.json** and kept
  going. One transient squatter permanently changed the panel's URL:
  every enrolled agent (and every install command / enroll token built
  from `panel.json` port + base path) now hits a dead address.
- `startHTTPSListener()` did the same for `tls_port`.
- The base path was never auto-changed (only the operator's setup form
  writes it) — verified, kept that way.

## The fix (this branch)

- `panel.json` gains `ports_finalized`. The FIRST successful resolution
  sets it (persisted) and logs:
  `listener ports finalized: HTTP <p>, TLS <p>, base path /<b> …`
- After that the ports are **frozen**: a busy port is a LOUD fatal error
  naming the fix — `Free the port, or set GRE_PANEL_PORT / panel.json
  port …` — and the process exits with the config UNCHANGED. No silent
  rebind, ever.
- Auto-switch now happens ONLY on the first start when the preferred
  port is taken, logged as `FIRST START: … auto-switched to … and SAVED`.
- Port knobs (explicit operator choices, always win at resolve time):
  `GRE_PANEL_PORT` (existing) and the new `GRE_PANEL_TLS_PORT`; both
  validated 1–65535. `panel.json` `port` / `tls_port` / `bind_host` /
  `http_bind_host` remain the persistent config.
- The second, redundant auto-switch at bind time (Main's `net.Listen`
  retry) was removed — a failure there is fatal with instructions.

## Verified (all ran)

- Unit tests (`listener_stability_test.go`): first-start auto-switch
  persists + finalizes; later start refuses (`listenerMoveAllowed()` ==
  false); free port keeps everything stable; base path untouched by the
  port machinery; `GRE_PANEL_TLS_PORT` override + invalid fallback;
  `ports_finalized` round-trips through panel.json.
- Live reproduction (throwaway panel, 127.0.0.1 only, cleaned after):
  start 1 binds 18791 and logs finalization; a socket squatter takes
  18791; start 2 exits with the FINALIZED message and panel.json still
  says `18791`; after the squatter leaves, start 3 serves health 200 on
  the SAME url.
- Note for e2e runners: run the panel with an explicit
  `GRE_PANEL_PORT`/`GRE_PANEL_TLS_PORT` in a free range (20000–30000 on
  the Windows dev host) — a squatted port now STOPS the panel instead of
  silently moving it.

## For operators (production impact)

Upgrades from older builds: the first start after the upgrade finalizes
whatever port is currently in panel.json — from then on the URL agents
hold cannot drift. To change a port deliberately: set the env var or edit
panel.json BEFORE starting (the panel binds the new value and the
installed agents need their panel URL updated the same way enroll gave
it to them).
