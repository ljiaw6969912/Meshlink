# Meshlink Product Onboarding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn Meshlink from an engineering console into a product onboarding flow with create network, invite device, join network, managed configuration, device list, and remote desktop entry.

**Architecture:** Add reusable onboarding services under `internal/onboarding` and keep the existing agent, runner, certificate, service, status, and RDP packages intact. UI layers call the onboarding service and generate ordinary agent JSON config so existing runtime behavior remains compatible.

**Tech Stack:** Go standard library, existing Meshlink packages, embedded HTTP UI, Windows Walk desktop UI, Go tests.

---

### Task 1: Transport Compatibility

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] Add `TransportConfig` with `protocol`, `listen`, `connect`, and `server_name`.
- [ ] Add tests proving hub configs can use `transport.listen`, spoke configs can use `transport.connect`, legacy fields still validate, and unsupported protocols fail clearly.
- [ ] Normalize validated configs so runtime code can continue reading `Listen`, `Connect`, and `ServerName`.
- [ ] Run `go test ./internal/config`.

### Task 2: CSR Certificate Flow

**Files:**
- Modify: `internal/certutil/certutil.go`
- Create: `internal/certutil/certutil_test.go`

- [ ] Add node key plus CSR generation helpers that write only the spoke private key and CSR on the spoke side.
- [ ] Add CSR signing helper that reads hub CA cert/key and returns a PEM node certificate without generating a private key on the hub.
- [ ] Test that the CSR public key matches the issued certificate and that IPv6 SANs are rejected.
- [ ] Run `go test ./internal/certutil`.

### Task 3: Onboarding Core

**Files:**
- Create: `internal/onboarding/paths.go`
- Create: `internal/onboarding/invite.go`
- Create: `internal/onboarding/manager.go`
- Create: `internal/onboarding/enroll.go`
- Create: `internal/onboarding/status.go`
- Create: `internal/onboarding/onboarding_test.go`

- [ ] Add product layout helpers for `configs`, `certs`, `invites`, `logs`, and `updates`.
- [ ] Add hub creation that initializes CA, signs hub cert, writes `configs/active.json`, and returns user-facing status.
- [ ] Add invite store with 128-bit token, six-digit code, 10-minute expiry, one-use default, five-failure invalidation, and audit fields.
- [ ] Add enroll handling that validates token and code, signs the spoke CSR, assigns the next `10.77.0.x` IP, writes device registry state, and returns CA/cert/config without any private key.
- [ ] Add spoke join helper that parses `meshlink://join` links, generates key/CSR locally, calls the enroll endpoint, writes CA/cert/key/config, and returns status.
- [ ] Add device list helper that reads agent status JSON and registry state.
- [ ] Run `go test ./internal/onboarding`.

### Task 4: Agent And API Integration

**Files:**
- Modify: `internal/agent/agent.go`
- Modify: `internal/agent/hub.go`
- Create: `internal/agent/enroll_test.go`
- Modify: `internal/ui/server.go`
- Create: `internal/ui/server_test.go`

- [ ] Start the enrollment HTTP handler when the hub runs and an invite store path is discoverable from the config directory.
- [ ] Expose `GET /enroll/health` and `POST /enroll/request` on the hub listener using HTTP-over-TLS detection before mesh frames.
- [ ] Add local UI endpoints for onboarding info, create hub, create invite, join network, list devices, and local listen diagnostics.
- [ ] Keep service installation/start actions delegated to existing `winservice` functions.
- [ ] Run `go test ./internal/agent ./internal/ui`.

### Task 5: Product Web UI

**Files:**
- Modify: `internal/ui/static/index.html`
- Modify: `internal/ui/static/app.js`
- Modify: `internal/ui/static/style.css`

- [ ] Replace the first screen with Meshlink, private remote desktop networking, `创建组网`, and `加入组网`.
- [ ] Add create-network form with network name, local name, port, connection mode, start button, status, public mapping hint, and invite button.
- [ ] Add invite modal with server address, code, expiry, invite link, copy actions, and close action.
- [ ] Add join-network form with invite link, code, local name, parsed connection mode, and join/start button.
- [ ] Add device list view with online/offline indicators, copy IP, diagnose, and remote desktop buttons.
- [ ] Move JSON config, certificate tools, service controls, logs, diagnostics, and updates into advanced sections.

### Task 6: Windows Desktop Product Flow

**Files:**
- Modify: `cmd/mesh-desktop/main_windows.go`

- [ ] Add top-level create/join/device tabs ahead of advanced controls.
- [ ] Wire create hub, create invite, join network, device list, RDP, diagnostics, and service actions through the onboarding package.
- [ ] Keep existing advanced tabs for JSON, certificates, service management, logs, diagnostics, and updates.

### Task 7: Verification

**Files:**
- Modify as needed based on verification results.

- [ ] Run `go test ./...`.
- [ ] Run `powershell -ExecutionPolicy Bypass -File .\scripts\build.ps1`.
- [ ] Start `mesh-ui` locally and verify the onboarding UI renders.
- [ ] Re-run focused tests for packages changed after any fix.
