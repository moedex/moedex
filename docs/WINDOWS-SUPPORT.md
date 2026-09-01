# Windows support — scoping & roadmap

What would it take to run moedex on Windows the way `scripts/install-macos.sh`
sets it up on a Mac? This doc audits the actual portability surface (grounded in a
`GOOS=windows` cross-compile), weighs the two viable strategies, and lays out a
phased plan with effort estimates.

**Status:** analysis / not started. Author: 2026-06-26.

---

## TL;DR

- The **engine is almost entirely portable Go already.** A `GOOS=windows go build ./...`
  fails in exactly **one** place: the mmap layer (`internal/diskstore`, 4 undefined
  `syscall` symbols). That's the whole hard compile blocker for the core.
- Two non-compile gaps follow: the **SIGHUP hot-reload** (compiles on Windows but
  never fires) and **`syscall.Getrusage`** (used only by the dev/CI parity harness).
- The **dense arm** (`-tags onnx`) is the one piece needing CGO + a native DLL +
  a C toolchain — it's optional and can come last. The **default pure-Go build
  (lexical + symbol) needs none of that.**
- Everything painful is in the **ops layer**, not the engine: launchd, the bash
  bootstrap, Homebrew, `chmod 0600`, `~/.zshrc`, `~/Library/LaunchAgents`.

**Recommendation — phase it:**

| Phase | Scope | Effort | Gets you |
|---|---|---|---|
| **0. WSL2** | Document + a small install script; run the existing Linux path inside WSL2 | **~0.5 day** | A working daemon on a Windows machine *today*, dense included |
| **1. Native, pure-Go** | mmap shim + portable reload + Windows service/scheduler + PowerShell bootstrap; dense OFF | **~3.5–5 days** | A first-class native lexical+symbol daemon, no VM, survives reboot |
| **2. Native, dense** | `onnxruntime.dll` + CGO/mingw build + ACL token + Windows CI | **~2–3 days** | Dense arm on native Windows |

If someone needs moedex on Windows *now*, **Phase 0 (WSL2)** is the answer and it's
basically free. Native (Phase 1) is the right investment only if a VM-free, reboot-
surviving Windows service is a real requirement.

---

## Strategy A vs B: WSL2 or native?

**WSL2** is the 10%-effort path. WSL2 is a real Linux kernel, so the engine, the
bash scripts, the `deploy/*.service` systemd units, mmap, SIGHUP, and CGO/onnx all
work unchanged — it *is* the Linux target. Costs: it's a VM (RAM overhead, a second
filesystem), and corpus I/O is only fast if the corpus lives on the WSL2 ext4 volume,
**not** on `/mnt/c` (the 9P bridge to the Windows drive is slow — a ~4 GB corpus on
`/mnt/c` would crawl). For a single dev this is great; as a "Windows product" it's a
Linux box in a trench coat.

**Native** gives a true Windows service that starts at boot with no VM, integrates
with the Service Control Manager and Task Scheduler, and stores data under
`%LOCALAPPDATA%`. It costs real porting work (below). The good news from the audit:
that work is small and contained.

**Recommendation:** ship Phase 0 (WSL2) as the documented answer immediately; pursue
native only when a non-VM Windows deployment is an actual requirement, and even then
do pure-Go (Phase 1) first — it delivers most of the value without the CGO tax.

---

## Portability audit (what the cross-compile actually says)

`GOOS=windows GOARCH=amd64 go build ./...` today:

```
# moedex/internal/diskstore
internal/diskstore/diskstore.go:629: undefined: syscall.Mmap
internal/diskstore/diskstore.go:629: undefined: syscall.PROT_READ
internal/diskstore/diskstore.go:629: undefined: syscall.MAP_SHARED
internal/diskstore/diskstore.go:640: undefined: syscall.Munmap
```

That is the *only* package that fails to compile. Everything downstream (server,
rank, contextwin, mcp, the daemon) is blocked solely by this transitive dependency.

**Already portable, no work:** `internal/{index,query,search,trigram,tokenindex,
symbol,rank,contextwin,mcp,embed(default),blobstore,setops}` and the HTTP/MCP daemon.
`setops` has a pure-Go fallback (the AVX2 kernel is amd64 + build-tagged). The doctor's
launchd probe is already guarded by `runtime.GOOS == "darwin"`. No hardcoded unix
paths in Go code — it uses `os.UserHomeDir()` + `filepath.Join` throughout.

---

## Engine gaps (Phase 1)

### 1. mmap — the one real compile blocker  *(~0.5–1 day)*

The entire platform-specific surface is tiny and contained in
`internal/diskstore/diskstore.go`:

```go
type mmapRegion struct { data []byte }
func mmapOpenMin(path string, minSize int) (*mmapRegion, error) {
    ...
    data, err := syscall.Mmap(int(f.Fd()), 0, size, syscall.PROT_READ, syscall.MAP_SHARED)
    return &mmapRegion{data: data}, nil
}
func (m *mmapRegion) Close() error { return syscall.Munmap(m.data) }
```

Everything else in diskstore reads `m.data []byte` sub-slices, so the contract to
preserve is just "give me a read-only `[]byte` view of the file."

**Fix:** split into build-tagged files, keeping the `[]byte` contract:
- `diskstore_mmap_unix.go` (`//go:build !windows`) — the current code, moved verbatim.
- `diskstore_mmap_windows.go` (`//go:build windows`) — `CreateFileMapping` +
  `MapViewOfFile(FILE_MAP_READ)` via `golang.org/x/sys/windows`, then
  `unsafe.Slice` the view pointer to a `[]byte` of the file size. `Close` does
  `UnmapViewOfFile` + `CloseHandle(mapping)`. The `mmapRegion` on Windows carries
  the extra mapping handle.

Adds one dependency (`golang.org/x/sys`). Alternative: `golang.org/x/exp/mmap`
(cross-platform, read-only) — rejected because it exposes `ReaderAt`, not a raw
`[]byte`, which would force a refactor of every sub-slice decode site. The build-tag
shim is the minimal change.

> Watch-out: zero-length / header-only files (the empty content store maps
> `contentHeaderSize` bytes) — `MapViewOfFile` of a 0-byte file fails on Windows;
> keep the existing `minSize` guard and special-case empty.

### 2. SIGHUP hot-reload — compiles, never fires  *(~0.5–1 day)*

`internal/app/servecmd/main.go` reloads the warm corpus on `syscall.SIGHUP` (3 sites:
`runMCP`, `runMCPHTTP`, `runHTTP`). `syscall.SIGHUP` *is* defined on Windows so it
compiles, but Windows never delivers it — so the daily refresh's "SIGHUP the daemon
to hot-swap" step would silently do nothing.

**Fix:** add a portable reload trigger and feed the existing reload goroutine from
it. Cleanest is an **authenticated admin HTTP endpoint** (`POST /admin/reload`,
reusing the bearer-token auth the daemon already has) — cross-platform, and the
refresh script calls it instead of `launchctl kill -HUP` on Windows. Keep SIGHUP on
unix. Refactor the three sites behind a single `reloadRequests <-chan struct{}`
fed by `signal.Notify(SIGHUP)` on unix and the endpoint everywhere. (`os.Interrupt`
already covers graceful stop cross-platform; prefer it over `syscall.SIGTERM`.)

### 3. `syscall.Getrusage` — dev/CI only  *(~0.25 day, optional)*

`internal/parity/corpus.go:352` reads max-RSS via `syscall.Getrusage(RUSAGE_SELF)`,
which doesn't exist on Windows. This is in the **parity harness** (a CI/dev gate),
not in any operational binary, so it doesn't block the daemon. Build-tag a Windows
stub (return 0, or use `runtime.MemStats`) only if you want to run `make parity` on
Windows — otherwise leave CI parity on Linux and skip this.

---

## Dense arm / CGO (Phase 2)  *(~2–3 days)*

The default build is pure Go and ships with zero ML deps — **none of this applies
unless you want the dense arm on native Windows.** With `-tags onnx`:

- `github.com/yalue/onnxruntime_go` is CGO and `dlopen`s the runtime at start. On
  Windows that's **`onnxruntime.dll`** (a Microsoft release artifact) instead of
  `libonnxruntime.dylib`. `configureDenseArm` already takes the path via
  `-onnx-runtime` / `ONNXRUNTIME_LIB_PATH`, so it's just a different file.
- Building `-tags onnx` on Windows needs **`CGO_ENABLED=1` + a C toolchain
  (mingw-w64 / TDM-GCC)**. CGO cross-compilation from macOS/Linux to Windows is
  painful; plan to **build on a native Windows runner**.
- The model (`go:embed` ~78 MB int8) and the forked tokenizer are pure Go → already
  portable.
- Ship `onnxruntime.dll` next to the binary or under `%LOCALAPPDATA%\moedex`, and
  put its directory on the DLL search path (or co-locate with `moe.exe`).

So Phase 2 = bundle the DLL + a Windows CI build job + ACL the token; the code
changes are near-zero.

---

## Ops layer — the `install-macos.sh` analogue (Phase 1)

This is where the real surface area is. Mapping each macOS piece to Windows:

| macOS | Windows native |
|---|---|
| launchd `com.moedex.serve` (KeepAlive) | **Windows Service** via SCM. Use `github.com/kardianos/service` (cross-platform service install from Go) or `sc.exe create … start= auto` + a wrapper. Auto-restart = recovery actions. |
| launchd `com.moedex.refresh` (StartCalendarInterval 13:00 local time) | **Task Scheduler** (`schtasks /create /sc daily /st 13:00`) running `refresh-corpus.ps1`. |
| `launchctl kickstart -p` (run refresh now) | `schtasks /run` or call `POST /admin/reload` after a manual refresh. |
| `scripts/install-macos.sh` (bash) | **`scripts/install-windows.ps1`** (PowerShell). |
| `scripts/refresh-corpus.sh` (bash) | `scripts/refresh-corpus.ps1` (or run the bash one under Git-Bash). |
| Homebrew `libonnxruntime` | download `onnxruntime.dll` release, or `winget`/`choco`/`vcpkg`. |
| `make install` → `~/.local/bin`, rm `~/go/bin` shadows | install `.exe`s to `%LOCALAPPDATA%\Programs\moedex`; add to user `PATH` via `setx`/registry; same shadow-removal idea. |
| `chmod 0600` token | **`icacls`** to grant only the current user (Go's `os.Chmod` only toggles read-only on Windows — not a real ACL). |
| `~/.zshrc` env block | user environment variables via `setx` (or `$PROFILE`). |
| `~/.moedex-state`, `~/.moedex` | `%LOCALAPPDATA%\moedex\index`, `%LOCALAPPDATA%\moedex\corpus` (Go already resolves via `os.UserHomeDir()`; the scripts/plists hardcode unix paths and get replaced). |
| `moedex doctor` launchd checks | swap the `launchctl print` probes for `sc query` / `schtasks /query`, guarded by `runtime.GOOS == "windows"` (the doctor framework is already OS-aware). |

`git` and `glab` both run on Windows, so the corpus tooling (`moedex corpus`) needs
no changes beyond paths.

---

## Phased plan

**Phase 0 — WSL2 (≈0.5 day).** Write `docs/INSTALL-WSL.md`: enable WSL2, install Go +
`onnxruntime` `.so`, clone, keep the corpus on the ext4 volume (not `/mnt/c`), run the
existing bash bootstrap (or `deploy/*.service` under systemd-in-WSL). Acceptance: the
daemon serves on `127.0.0.1:8081` and `doctor` is green inside WSL.

**Phase 1 — native pure-Go (≈3.5–5 days).** mmap shim (1) + portable reload (2) +
service/scheduler + `install-windows.ps1` + doctor Windows checks (+ optional parity
stub). Dense OFF. Acceptance: `GOOS=windows go build ./...` clean; `moe.exe serve`
runs as a service across reboot; `schtasks` refresh works; `moe.exe doctor`
green on Windows.

**Phase 2 — native dense (≈2–3 days).** Bundle `onnxruntime.dll`, add a Windows CI
runner that builds `-tags onnx` with mingw, ACL the token, test the dense arm.
Acceptance: `dense=true` daemon serving embeddings on native Windows.

**Cross-cutting — CI gate (≈0.25 day, do first):** add `GOOS=windows go build ./...`
(and `go vet`) to CI. It fails today (proving the mmap point) and becomes the
regression guard that keeps the tree Windows-clean as Phase 1 lands.

---

## Risks / open questions

- **mmap lifetime on Windows.** The view must be unmapped *and* the mapping handle
  closed; the file handle can close after mapping. Get this wrong and you leak handles
  on every refresh reload (the unix side already had this exact class of bug — see the
  `reload.go` close-on-swap comment). Test under repeated reloads.
- **CGO cross-compile is not worth fighting.** Build dense on a real Windows runner;
  don't try to cross-compile `-tags onnx` from macOS.
- **Antivirus / SmartScreen** may flag an unsigned `.exe` service. Code-signing is a
  separate (org) concern if this ships beyond a dev box.
- **Path-length / separators** are handled by `filepath`, but double-check any place
  that builds paths by string concat (none found in the engine today).
- **Is native even wanted?** If the only Windows users are developers, Phase 0 (WSL2)
  may be the whole answer and Phases 1–2 are unnecessary.
