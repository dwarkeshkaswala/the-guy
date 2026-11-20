# The Guy Desktop — Packaging & Platforms

## Toolchain (pinned)

- Wails **v3.1.0** (module `github.com/wailsapp/wails/v3`, CLI
  `go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0`)
- Go 1.26.x, Node 18+ / npm, WebView2 runtime (preinstalled on Windows 10/11)

## Windows x64 (tested)

```
wails3 task build          # via Taskfile: bindings + frontend + go build + icon
# equivalent manual path:
cd frontend && npm install && npm run build && cd ..
go build -tags production -ldflags "-s -w -H windowsgui" -o the-guy-desktop.exe .
```

- Pure Go: `CGO_ENABLED=0`; no external compilers needed.
- Binary size ≈ 15 MB (embeds the frontend bundle: ~465 KB JS / 43 KB CSS).
- Installer targets (NSIS/MSIX assets under `build/windows/`) are provided by
  the Wails template but **not yet produced or signed** — unsigned `exe` only.
- Startup measured (dev machine, i9-12900H, WebView2 157): process start →
  WebView2 environment created in ~35 ms; UI interactive well under a second.

## Other platforms (compile-target status, honestly)

macOS (Intel/Apple Silicon) and Linux (x64/arm64) build with the standard Wails
v3 toolchain (macOS needs Xcode; Linux needs WebKitGTK 4.1 dev packages, which
require CGO). **They have not been run or smoke-tested in this environment** —
cross-compiling is not performance validation. Treat them as compile targets
until tested on real machines/VMs.

## CI

`.github/workflows/ci.yml` runs `go vet`, `go test -race ./...` and builds both
`guy` (all platforms) and `the-guy-desktop` (windows-latest) on every push/PR.
Frontend typecheck (`tsc --noEmit`) is part of both `npm run build` and CI.

## Known limitations / backlog

1. **No daemon mode.** Window closure interrupts owned runs (reconciled on next
   start). A minimal `guy daemon` with authenticated loopback IPC is the main
   follow-up.
2. Windows code signing / macOS notarization not configured (no certificates).
3. Scheduler pause/resume not implemented (cancellation + dependency gating
   cover current flows).
4. Default Wails template icon; branded icon pending.
5. WebView2 runtime is a hard prerequisite on Windows (satisfied by default on
   supported Windows versions; the standard WebView2 bootstrapper can be
   bundled later).
