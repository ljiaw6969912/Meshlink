# Meshlink Stability Release Acceptance Evidence

- Date: 2026-07-23
- Task: TASK-009 / implementation plan Task 8
- Environment: local Windows amd64, Go 1.26.1
- Scope: local verification only; no network access, service launch, Hub manipulation, release, or multi-machine Windows exercise was performed.

## Evidence policy

Automated tests and source-contract tests are recorded as implementation support only. They are not treated as substitutes for the real Windows desktop, service, restart, clipboard, RDP, or multi-machine scenarios required by the approved plan. A criterion that still needs one of those observations is marked `BLOCKED`.

## Automated verification

| Check | Result | Evidence |
| --- | --- | --- |
| Complete Go suite | PASS | `go test -count=1 ./...` completed with exit code 0 after the scoped fix; all tested packages passed. |
| Static analysis | PASS | Initial `go vet ./...` failed on 25 unkeyed `declarative.Size` literals. Commit `cdc899c` converted them to keyed `Width`/`Height` fields and updated the matching source-contract tests. Final `go vet ./...` completed with exit code 0 and no diagnostics. |
| Development build | PASS | `powershell -ExecutionPolicy Bypass -File .\scripts\build.ps1 -Mode Development` completed with exit code 0. It built Meshlink `0.1.5` in development mode at `2026-07-23T09:36:48Z`; code signing was disabled as expected for this mode. |

Build artifacts recorded by `bin/build-metadata.json`:

| Artifact | SHA-256 |
| --- | --- |
| `mesh-agent.exe` | `ee8b79446e814127f8517fd2662eb8abd576d9a8036512481cbb9637e44a4094` |
| `mesh-cloudhub.exe` | `d8dd99c201b46f5ae8a9fade3a8f42a31a8723eb37c2d12a68cb48d2afc6b4fb` |
| `mesh-desktop.exe` | `705e20a57b7617e2b6240e0e3204eca39ae27bd0dd5aea460bd3067a15f662a2` |
| `mesh-update-server.exe` | `00d964defc041cc0f302512bc4ff11d2617579649435e688942bc78b747985ff` |
| `meshctl.exe` | `df3511b3601ce1a0559144c3ea4be597c76d1d8061b60de8fe9dd75a81ff117e` |
| `linux/mesh-agent` | `1343480972a58f1a8b6398fba84b2546ae5d96b29c8c04873aa5332d50ad3afc` |

## Acceptance index

| AC | Result | Evidence | Risk |
| --- | --- | --- | --- |
| AC-001 | BLOCKED | Supporting test `TestInviteOutputHasBoundedHeight` passed in the complete suite. The desktop was not launched and no long invitation was scrolled or copied through the real Windows clipboard. | Medium: clipping, scrolling, focus, or clipboard integration could still differ at runtime. |
| AC-002 | BLOCKED | Supporting tests `TestInfrastructureModes`, `TestStatusStoreDropsInfrastructureAndMarksAllPeersOffline`, and `TestDevicesHideInfrastructureAndRespectReconnectState` passed. No real Hub plus multiple terminal network was joined and counted. | Medium: real roster role data or UI projection could still include infrastructure nodes. |
| AC-003 | BLOCKED | Supporting status-store and reconnect tests passed in the complete suite. No controllable Hub outage was performed with a real Spoke. | High: disconnect detection timing and live UI state remain unobserved. |
| AC-004 | BLOCKED | Reconnect and roster replacement logic passed automated coverage. No real Hub path was restored and no fresh post-reconnect roster was observed. | High: transport recovery or stale live state could diverge from the tested transition. |
| AC-005 | BLOCKED | Automated coverage supports conservative reconnect state and offline peers. No sustained real reconnect failure or manual reconnect action was exercised. | High: retry behavior, error presentation, and manual recovery remain unobserved. |
| AC-006 | BLOCKED | Supporting lifecycle UI/source tests and status tests passed. The real Windows client/service was not disconnected and reconnected while preserving identity. | High: service state or persisted identity could behave differently on a real installation. |
| AC-007 | BLOCKED | `TestLeaveStopsThenUninstallsThenClearsIdentity`, onboarding leave tests, and leave API tests passed. No installed Windows service was stopped/uninstalled, no application restart was performed, and no new-invite rejoin was attempted. | High: destructive cleanup, restart persistence, and recovery require a controlled Windows host. |
| AC-008 | BLOCKED | The confirmation text and distinct action source contracts passed. No real confirmation dialog was cancelled while snapshots of connection, identity, and nodes were compared. | Medium: event wiring or dialog behavior could still mutate state. |
| AC-009 | BLOCKED | `TestOfficialHubMVPDefaultsOffAndRequiresExplicitTrue` and capability/UI source contracts passed; the development build used the default environment. The desktop was not launched with `MESHLINK_ENABLE_OFFICIAL_HUB_MVP` unset to visually confirm absence. | Medium: runtime menu composition or environment handling remains visually unverified. |
| AC-010 | BLOCKED | Product-flag and capability tests passed. The desktop was not launched with `MESHLINK_ENABLE_OFFICIAL_HUB_MVP=1` to verify the internal entry and its test labeling. | Medium: enabled-state visibility and wording remain visually unverified. |
| AC-011 | BLOCKED | `TestCheckRDPTargetStopsBeforeDialWhenTunnelOffline` and the RDP API coverage passed. Diagnostics were not run from a genuinely disconnected Windows client while observing that no RDP port probe occurred. | High: real routing, socket, and desktop diagnostic integration remain unobserved. |
| AC-012 | BLOCKED | `TestDesktopRefreshUsesDeviceListNetworkStateEvenWhenEmpty` and `TestStaticWebLifecycleAndAutomaticRefreshContract` passed. No live desktop or web session observed status changes without manual refresh. | Medium: timer, event-loop, or rendering behavior could fail outside source-contract coverage. |

## Environment gaps and next evidence

- A controlled Windows environment with one Hub and at least one Spoke is required for AC-002 through AC-007, AC-011, and AC-012.
- A Windows desktop session with clipboard access is required for AC-001, AC-008, AC-009, and AC-010.
- AC-007 additionally requires an installed test service, application and service-manager restart, persistence inspection, and a rejoin using a new invite.
- No criterion is marked `PASS` from unit, integration, static source, or build evidence alone.
