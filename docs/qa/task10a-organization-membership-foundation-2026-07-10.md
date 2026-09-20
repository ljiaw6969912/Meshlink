# Task 10A Organization Membership Foundation QA

Date: 2026-07-10
Baseline: `27826a0 TASK 9D subscription quota client experience`
Commit target: `TASK 10A organization membership foundation`

## Scope

- Added Organization and Membership foundation in `internal/cloudhub`.
- Added owner/admin/member role placeholders and active/suspended/removed membership states.
- Added organization lifecycle APIs for create, get, member list, member invite, invite accept, invite revoke, organization suspend/resume, and member suspend/resume/remove.
- Did not implement 10B RBAC matrix, device grouping, or connection authorization.
- Did not implement 10C audit query/log cleanup, 10D bulk deployment, or 10E enterprise authorization.
- Did not change RDP/P2P/Relay data plane, payments, email/SMS sending, UI, or external test hosts.

## Security And Privacy Checks

- Organization invites are one-time and short-lived by default.
- Invite plaintext token/code are returned only in the creation response.
- Server-side organization invite storage keeps only irreversible token/code digests.
- Organization invite digest fields are excluded from JSON responses.
- Organization audit metadata records actor account, organization, target account, action, result, and timestamp through existing audit event fields.
- Organization audit metadata does not include invite plaintext, invite digests, passwords, tokens, private keys, subscription provider customer/subscription IDs, or free-form reason text.

## Quota And Atomicity Checks

- Member quota is evaluated from the organization owner account's effective plan.
- `unavailable` member quota rejects organization creation.
- finite member quota counts non-removed members, including owner, and rejects atomically when full.
- `unlimited` member quota behavior is covered by the member-count quota evaluator.
- unresolved `contract_custom` member quota rejects safely and is not treated as unlimited.
- Invite acceptance and member count/write happen in one `MemoryStore.AcceptOrganizationInvite` lock to prevent concurrent over-admission.
- Legacy accounts without organizations keep existing network/invite/device/AccountPolicy behavior.

## Test Coverage

- Organization creation, lookup, owner auto-membership, and member listing.
- Owner invariants for primary owner suspend/remove refusal.
- Invite digest storage, expiration, revocation, replay, organization mismatch, account mismatch, and public/audit redaction.
- Member accept, suspend, resume, and remove.
- Account frozen/banned and organization suspended gates for new member relationships.
- Member quota unavailable/finite/unlimited/custom semantics and concurrent acceptance not exceeding quota.
- HTTP server and client organization/member closed loop.
- Legacy no-organization account compatibility.

## Verification

- `go test -count=1 ./internal/cloudhub`: pass
- `go test -count=1 ./...`: pass
- `git diff --check`: pass
- `git diff --cached --check`: pass

No temporary Go overlay was used.
