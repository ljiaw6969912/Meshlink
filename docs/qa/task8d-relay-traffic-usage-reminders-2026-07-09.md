# Task 8D Relay Traffic Usage Reminders QA

Date: 2026-07-09

## Coverage

- Added cloudhub account-summary relay usage reminder model for quota usage thresholds:
  - below 80%: no reminder
  - 80% to 99%: `approaching`, "接近中继流量上限"
  - 100% to 119%: `reached`, "中继流量已达上限"
  - 120% and above: `overage`, "中继流量明显超额"
- Reminder text includes plain-language user context:
  - current connection is using relay
  - used bytes and limit bytes
  - possible effect when the limit is reached or exceeded
  - clear note that this is not a normal network error
  - suggested handling: try direct connection first, use self-hosted/admin relay, and future upgrade entry placeholder
- Official Hub device list API now returns `relay_usage_reminder` when account summary is available.
- Official Hub device list UI renders the reminder in a dedicated banner with responsive wrapping.
- Static UI test covers the 80% / 100% / 120% labels and checks the reminder renderer does not expose low-level transport terms or secret markers.

## Boundaries

- The reminder is summary/display only. It consumes existing relay usage and quota fields.
- The Official Hub device list still refreshes devices if the summary endpoint is unavailable; in that case it simply hides the reminder.
- Local runtime device rows still show connection path and relay bytes from runtime status, but account quota reminders require Official Hub account summary data.
- No real billing, payment, subscription purchase, or commercial plan flow was added.
- No RDP data bridge changes were made.
- No real public STUN/TURN/QUIC, public relay deployment, or external network test information was added.
- No test-machine passwords, tokens, private keys, or public test endpoints were written.

## Verification

- Plain `go test` in this machine fails before repo tests because the local Go standard library is damaged:
  - `C:\Program Files\Go\src\go\ast\ast.go:606:44: undefined: tgken`
- Used the temporary local overlay:
  - `C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json`
- Passed with overlay:
  - `go test -count=1 ./internal/ui ./internal/relay ./internal/cloudhub ./internal/onboarding`
  - `go test -count=1 ./...`

## Unfinished

- The "升级入口" is intentionally a placeholder only.
- No production quota purchase, billing enforcement changes, or subscription UI was implemented.
