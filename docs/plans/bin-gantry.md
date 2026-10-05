# Plan: `bin/gantry` boots an app with no CLIs installed

### Goal
`git clone` an app, `bin/gantry dev`, and Houston's existing dev proxy serves it at `http://<name>.localhost`. The machine already has a shell, curl, and Docker. It does not have gantry or Houston installed.

### Complexity
Over threshold — a producer must exist before the consumer works for real (trigger 3). Batch 1 is the script an app runs. Batch 2 is the gantry release that publishes the binaries the script downloads. Houston already publishes its binaries.

### Scope
Batch 1: `gantry new` writes an executable `bin/gantry`. The first run reads the gantry version from the nearest `go.mod`, downloads that release's CLI and the Houston release pinned in the script, checks `SHA256SUMS`, and execs gantry with `.gantry/bin` first on `PATH`. A second run uses the cache. A local gantry checkout (`replace` or `go.work`) uses that checkout's `bin/gantry` and never downloads a different gantry.

Batch 2: tagging gantry builds `gantry-{darwin,linux}-{arm64,amd64}` and `SHA256SUMS` onto the GitHub Release, the same shape Houston already ships.

**Cut:** mise. It would be a prerequisite the clone doesn't have.
**Cut:** compiling a missing release inside the script. A tag with no asset fails clearly. Releases before the first tagged CLI build have no asset.
**Boundary:** `gantry new` still uses a gantry you installed, and Houston on `PATH`, to create the app. The script is how an existing app runs.

### Slice
Batch 1 — `bin/gantry dev` downloads the pinned CLIs (or refuses) and execs.

### Design (short)
The committed file is a POSIX script. Binaries land in gitignored `.gantry/bin/`, named `gantry-vX.Y.Z-$os-$arch` and `houston-vX.Y.Z-$os-$arch`, with a symlink `gantry` or `houston` aimed at the one in use. Download goes to a pid-unique partial, checksums, then `rename`s into the versioned name, then `rename`s the symlink. Two processes installing the same version both end on those bytes. A failed checksum deletes the partial and leaves the previous symlink alone, and the script exits before exec.

`GANTRY_BIN` and `HOUSTON_BIN` skip that tool's download. `GANTRY_OS`, `GANTRY_ARCH`, `GANTRY_RELEASE_BASE`, and `HOUSTON_RELEASE_BASE` exist so tests can pin a platform and a local release server.

Asset names match Houston's: `houston-darwin-arm64`, and the same pattern for gantry. `v0.5.4` is the Houston pin `gantry new` writes (the current release, which has those assets).

### Contract pin
Script inputs: args passed through to gantry; `go.mod` require `github.com/scttymn/gantry vX.Y.Z` or a path `replace` / `go.work` `use` of a directory whose module is `github.com/scttymn/gantry`; Houston pin `vX.Y.Z` in the script.

Invalid: version that is not `v<digits>.<digits>.<digits>`; OS other than `darwin` or `linux`; arch other than `amd64` or `arm64`; no `go.mod` at or above the app; `go.mod` with no gantry require; checksum mismatch; `SHA256SUMS` line whose filename is not exactly the asset; HTTP 404.

Effects: `.gantry/bin` gains the versioned binary and the symlink only after the checksum matches. Exec happens only after both tools resolve. `PATH` starts with `.gantry/bin` so gantry's `LookPath("houston")` finds the sibling.

### Adversarial AC
- First run downloads both, checks the exact `SHA256SUMS` line, execs `gantry` with the original args, and `houston` resolves inside `.gantry/bin`.
- Second run does not touch the network.
- Wrong checksum, or a sums file that only names a lookalike filename, installs nothing.
- A failed upgrade to a newer gantry version leaves the previous binary in place and does not exec.
- A 404 names the version and platform and leaves no versioned binary.
- A pseudo-version, a missing require, a missing `go.mod`, or a bad arch exits before any HTTP.
- An app directory with no `go.mod` uses the parent module's require.
- `GANTRY_BIN` and `HOUSTON_BIN` run those binaries and do not download.
- A `replace` or `go.work` checkout with no `bin/gantry` exits, does not download gantry, and writes no `.gantry`.
- The same checkout with `bin/gantry` runs that file.
- Two processes installing one version both exec the same bytes.
- `gantry new` writes `bin/gantry` executable, ignores `.gantry/`, and tells you to run `bin/gantry dev`.

### Templates filled
Crash: the durable step is the rename of a checksummed partial onto the versioned filename. A crash before that leaves the partial (pid-unique, not the live name). Retry downloads again. A crash after the rename and before the symlink swap leaves a complete versioned file; the next run sees it and only moves the symlink. The script does not treat "symlink already points at an older version" as success when `go.mod` asks for a newer one.

Concurrency: two processes, same version, empty cache. No lock. Each writes a unique partial, checksums, renames onto one shared versioned path (same bytes), then renames its own symlink over `gantry`. End state: the symlink targets that version and the file matches the checksum. HTTP is not done while holding any lock, because there is no lock.

### AC ↔ test map
| AC | Test file | Test | Lens |
|----|-----------|------|------|
| First run downloads, checksums, execs, houston on PATH | `cmd/gantry/binstub_test.go` | `TestBinstubRunsPinnedCLIs` | Contract |
| Second run does no HTTP | `cmd/gantry/binstub_test.go` | `TestBinstubSkipsDownloadWhenCached` | Repair |
| Bad checksum or lookalike sums name installs nothing | `cmd/gantry/binstub_test.go` | `TestBinstubRejectsBadChecksum` | Contract |
| Failed upgrade keeps the old binary and does not exec | `cmd/gantry/binstub_test.go` | `TestBinstubKeepsGoodBinaryWhenRefreshFails` | Crash gap |
| 404 names version and platform, no binary | `cmd/gantry/binstub_test.go` | `TestBinstubRejectsMissingAsset` | Contract |
| Pseudo-version, no require, no go.mod, bad arch: no HTTP | `cmd/gantry/binstub_test.go` | `TestBinstubRejectsBadPins` | Preconditions |
| App dir uses the parent module's require | `cmd/gantry/binstub_test.go` | `TestBinstubUsesParentModule` | Contract |
| GANTRY_BIN and HOUSTON_BIN skip download | `cmd/gantry/binstub_test.go` | `TestBinstubHonorsBinOverrides` | Contract |
| Checkout without bin/gantry: no download, no .gantry | `cmd/gantry/binstub_test.go` | `TestBinstubRefusesLocalCheckout` | Preconditions |
| Checkout with bin/gantry runs it | `cmd/gantry/binstub_test.go` | `TestBinstubUsesCheckoutCLI` | Parity |
| Two processes, one version, both exec | `cmd/gantry/binstub_test.go` | `TestBinstubConcurrentInstall` | Concurrency |
| gantry new writes an executable bin/gantry and says to run it | `cmd/gantry/binstub_test.go` | `TestNewWritesExecutableBinstub` | Contract |

### Deploy notes
`.gantry/` is gitignored and dockerignored. The script is the same text on every platform. Binaries exist on a gantry release only after Batch 2. Houston `v0.5.4` already has them.

### Later batches
Batch 2 — gantry's release workflow and Dockerfile `release` stage publish the four CLIs and `SHA256SUMS`. Proved by `TestReleasePublishesBinstubAssets` (the Dockerfile and the workflow name the same `gantry-$os-$arch` assets the script requests) and by `docker build --target release` producing the four binaries.

### Agent loop checkpoints
- Red tests → implement-through (asked to make the clone-and-run path work)
- Map green → scotty-review
- Ready → touched-file lint + `go test ./cmd/gantry`
