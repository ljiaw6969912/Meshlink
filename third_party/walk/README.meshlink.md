# Meshlink Walk patch

Source: `github.com/lxn/walk` at `v0.0.0-20210112085537-c389da54e794`
(commit `c389da54e794`). The root Go sources, `declarative`,
AUTHORS, LICENSE and upstream README are copied unchanged except for
the two lifecycle changes in `layout.go` described below. Examples,
tools, localization data and documentation assets are not needed to build Meshlink.

The root module replaces Walk with this directory so normal Go builds,
tests and release scripts all use the same fix without modifying the
machine's module cache. The added `go.mod` pins dependencies to the
versions already selected by Meshlink.

## Layout shutdown fix

`startLayoutPerformer` used to close its worker result channel while
`layoutTree` could still be sending, causing `panic: send on closed channel`
when a window was disposed. A worker that had already finished its layout
also could not cancel an unconsumed result send.

- Leave the private worker result channel open when the coordinator exits.
  No receiver waits for it to close; it is collected after workers exit.
- Make the final result handoff cancellation-aware, so workers terminate
  when the window closes or a later layout supersedes their work.

`layout_shutdown_test.go` uses `testing/synctest` to put a real layout worker
at the result handoff and then cancel it. It fails deterministically with
the original implementation and passes with the patch. Run from the
repository root:

```powershell
go test github.com/lxn/walk -count=1
go test ./cmd/mesh-desktop -run TestDesktopRapidLayoutAndClose -count=3
```

Both Windows build scripts include the local dependency tests in their
test stage. The desktop regression also exercises real hidden windows
through repeated layout and disposal. The original BSD license is retained
in LICENSE and reproduced in Meshlink's distributed THIRD_PARTY_NOTICES.md.
