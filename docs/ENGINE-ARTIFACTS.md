# Engine artifacts — upload, signing, and delivery (R5)

This document is for the operator who ships engine binaries (for example
the FRP engine, `frps`/`frpc`) to tunnel servers through the Quantum
panel. No prior crypto knowledge needed; every step is copy-paste.

## The one idea to understand

An agent will only run an engine binary it can verify. The binary arrives
as a **tar.gz** plus a **detached signature** made with your **release
private key**. Agents carry the matching **public key pinned at build
time**. A compromised panel or a tampered download can therefore never
make an agent run a fake binary — the signature check fails closed.

- **Private key** — stays on your signing machine (laptop/USB). It NEVER
  touches the panel, any repo, or any server.
- **Public key** — safe to store everywhere: the panel reads it to check
  uploads; agent builds pin it at compile time.

## Step 1 — make a release key (once)

On your signing machine (with `openssl` 1.1+ or 3.x):

```bash
openssl genpkey -algorithm ed25519 -out engine-release.key      # PRIVATE — keep offline, chmod 600
openssl pkey -in engine-release.key -pubout -out engine-release.pub
```

## Step 2 — give the panel its public half

The panel expects the public key as **base64 of the DER "SPKI" form**
(not the raw 32 bytes — a raw key is refused with a clear error):

```bash
openssl pkey -pubin -in engine-release.pub -outform DER | base64 -w0 > engine-pub.b64
scp engine-pub.b64 panel:/etc/gre-panel/engine-artifacts/engine-pub.b64
```

Check the panel agrees — the Engines tab in `/mesh` shows the build
snippet once the key is readable (or `GET /api/mesh/engines/keysnippet`).

## Step 3 — pin the same key into agent builds

The Engines tab shows the exact snippet, which looks like:

```bash
go build -ldflags "-X github.com/Dark-modem/quantum-agent/internal/agent.engineArtifactPubKeyB64=<key_b64>" ./cmd/agent
```

The offline release builder already does this — see
`quantum-release/scripts/build-release.sh`.

## Step 4 — sign an artifact

With the artifact you built (e.g. `frp-0.61.1-linux-amd64.tar.gz`):

```bash
# sha256 (the panel recomputes it; agents verify it again on their side)
sha256sum frp-0.61.1-linux-amd64.tar.gz

# detached ed25519 signature over the EXACT artifact bytes (OpenSSL 3.x)
openssl pkeyutl -sign -inkey engine-release.key \
  -rawin -in frp-0.61.1-linux-amd64.tar.gz \
  -out frp-0.61.1-linux-amd64.tar.gz.sig

base64 -w0 frp-0.61.1-linux-amd64.tar.gz.sig   # this string goes into the upload form
```

## Step 5 — upload through the panel

Open the panel → `/mesh` → **Engines** tab → fill in:

| Field | Example | Notes |
|---|---|---|
| engine | `frp` | lowercase id, matches the agent's engine catalog |
| version | `0.61.1` | your tag; slots are per (engine, version, arch) |
| arch | `amd64` or `arm64` | the Germany test server is arm64, Iran is x86_64/amd64 |
| download name | `frp-0.61.1-linux-amd64.tar.gz` | optional; lowercase, no path separators |
| detached signature | (the base64 string from step 4) | 64-byte ed25519 signature, base64 |
| file | the tar.gz | up to 256 MiB |

The panel recomputes sha256, verifies the signature against the
configured key, and refuses:

- a signature that does not verify (wrong key / wrong bytes),
- a re-upload of the **same version+arch with a different sha256**
  (pick a new version or remove the old artifact first),
- unsafe download names (anything with path separators or uppercase).

Files are stored under `<configDir>/engine-artifacts/` with `0600`;
uploads are audited (ids only — never contents).

## What agents do with it

Agents read `GET /api/agent/engine-artifacts?engine=frp&goarch=amd64`
(unauthenticated manifest: engine, version, sha256, size, signature) and
download the bytes from `…/file/<name>`. The agent then verifies sha256
AND the ed25519 signature against its **build-pinned** key before
extracting anything — a malicious or broken panel cannot slip a bad
binary through.

The /mesh Engines tab warns when an engine is enabled in a topology but
a connected agent's arch has **no signed artifact** — resolve the
warning before expecting tunnels to come up on that agent.

## Iran–Germany link quality (practical note)

The Iran↔Germany link corrupts long transfers at some hours. If an
upload to the panel fails or an agent download keeps failing sha256,
transfer the artifact to the panel host in **2 MB chunks with per-chunk
checksums** (see the RUNBOOK-FA chunked-copy section) and upload from
local disk — the panel path itself is one LAN-fast HTTP POST.
