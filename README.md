# GPU Grid Worker (`gpu-grid-worker`)

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](./LICENSE)

Auditable, cross-platform (`windows/amd64` and `linux/amd64`), zero-CGO (`CGO_ENABLED=0`) GPU provider daemon and desktop GUI for **GPU Grid**.

Every line of code that runs on a provider's workstation or server lives in this repository so contributors and security researchers can inspect, compile from source, and verify `SHA-256` binary checksums independently.

---

## Security & Host Isolation Guarantees

1. **Loopback-Only Local Control Plane (`127.0.0.1:17771`)**:
   - Never binds to `0.0.0.0`.
   - Enforces single-instance physical host lock, strict `Host` + `Origin`/`Sec-Fetch-Site` DNS-rebinding & CSRF protection, and `X-Grid-Worker-Local: 1` on mutating requests.
   - Uses RFC 8252 loopback OAuth2/PKCE (`S256`) for browser sign-in (`GET /auth/callback`).
2. **Content-Addressed Storage (`SHA-256` CAS) & Archive Confinement**:
   - Every downloaded model weight and runtime pack is verified against its expected `SHA-256` digest before activation.
   - Zip/tar.gz extraction and CAS storage enforce strict path-traversal (`Zip Slip`) and symlink confinement guards plus SSRF dial-time IP validation.
3. **Subprocess Sandboxing & Deterministic Cleanup**:
   -Inference engines (`llama-server`, `ggml-rpc-server`, `diffusion`, `whisper`, and optional sidecars) run with sanitized environment variables (sensitive `*_TOKEN`, `*_SECRET`, and `*_PASSWORD` keys stripped) and deterministic teardown so zero orphan processes hold VRAM or ports on exit.
4. **Outbound Reverse WebSocket Tunnel (`/ws/worker`)**:
   - Connects outbound over `wss://` (with `http://`/`ws://` restricted to loopback and RFC 1918 private LANs unless explicitly overridden for local testing) — zero inbound firewall port-forwarding required.
5. **Cryptographic Dynamic Network Bootstrap (`Ed25519` Signed `network.json`)**:
   - Discovers and updates the active Gateway endpoint across redundant HTTPS mirrors (`/.well-known/gpu-grid-network.json`, GitHub Pages, and GitHub Releases) verified against an embedded `Ed25519` root public key and monotonic anti-rollback `version` counter while preserving existing `wk_...` provider tokens across domain migrations.
   - Explicit `--gateway` / `GPU_GRID_GATEWAY` flags and manual GUI / `PATCH /api/config` overrides pin the target endpoint (`gatewayPinned: true`) and bypass automatic discovery for private or offline deployments.

---

## Repository Layout

- `cmd/worker` — Composition root for desktop GUI (`modernc.org/tk9.0`) and headless CLI modes.
- `cmd/bootstrap-sign` — Offline `Ed25519` key generation, manifest signing, and verification utility for `network.json`.
- `pkg/domain` — Pure standard-library domain types, finite state machines (`fsm`), W3C `traceparent` propagation (`trace`), typed error catalog (`derr`), and Worker port contracts (`nodecore`).
- `pkg/protocol` — Canonical `ControlMsg` JSON frames and 4-byte Big-Endian binary stream framing (`solo_http`, `rpc`, `mesh_relay`).
- `pkg/wsconn` — Concurrency-safe WebSocket writer (`writeMu` serialization).
- `pkg/application/worker` — Session lifecycle, autopilot model predownload, idle detection, PKCE login, and `SOLO`/`MESH` supervision.
- `pkg/infrastructure` — CAS store, Vulkan GPU hardware probing, idle detector, engine runners (`llama`, `diffusion`, `whisper`, `engines`), reverse stream demuxer (`tunnel`), circuit breaker (`resilience`), and structured JSON logging (`observability`).
- `pkg/presentation` — Offline desktop GUI (`workergui`), loopback HTTP API (`workerhttp`), and `en`/`pt-BR` error localization (`workeri18n`).
- `tests/{arch,unit,integration}` — Architecture/leak ratchets, zero-I/O unit tests, and subprocess/CAS/tunnel integration tests.

---

## Reproducible Build & Verification (`CGO_ENABLED=0`)

### Prerequisites
- Go `1.26+` (pure Go; no C/C++ compiler required).

### Build From Source (Windows PowerShell / CMD)
```powershell
.\build.bat
```
Produces:
- `dist\gpu-worker-windows-amd64.exe`
- `dist\gpu-worker-linux-amd64`
- `dist\SHA256SUMS`

### Build From Source (Linux / macOS)
```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/gpu-worker-linux-amd64 ./cmd/worker
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w -H=windowsgui" -o dist/gpu-worker-windows-amd64.exe ./cmd/worker
sha256sum dist/gpu-worker-windows-amd64.exe dist/gpu-worker-linux-amd64
```

### Run Architecture, Unit & Integration Suites
```powershell
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test -race ./...
```

---

## CLI Usage

```bash
# Probe local Vulkan GPUs
./dist/gpu-worker-linux-amd64 --list-devices

# Headless server daemon
./dist/gpu-worker-linux-amd64 \
  --gateway https://your-gateway.example.com \
  --token wk_your_worker_token \
  --name node-01 \
  --devices Vulkan0 \
  --autostart \
  --headless

# Sign and verify a dynamic network bootstrap manifest (network.json)
go run ./cmd/bootstrap-sign \
  -sign \
  -key /path/to/network-bootstrap-ed25519.json \
  -gateway https://api.example.com \
  -version 1 \
  -out network.json

go run ./cmd/bootstrap-sign -verify -in network.json
```

---

## License & Trademark

Licensed under the [Apache License, Version 2.0](./LICENSE). See [NOTICE](./NOTICE) for trademark terms regarding the **GPU Grid** name and branding.
