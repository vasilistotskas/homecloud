# homecloud — design

Status: **P0 (foundation)** · Last reviewed: 2026-10-05

## 1. Goal

Make a local drive (E:) the single, verified home of the owner's iCloud data — iCloud Photos (≈3,765 assets / 315 GB, 285 GB video), iCloud Drive, and iPhone backups — while iCloud itself keeps only the most recent 30 days. Fully automatic, auditable, and fail-closed: nothing is ever removed from iCloud unless it is provably safe on disk.

## 2. Principles

1. **One code path per job.** No fallback sources, no duplicate stacks, no compatibility shims. Abstractions only when a second real implementation exists.
2. **Nothing hardcoded.** Paths, ports, limits, schedules and pinned tool versions live in a strictly validated TOML config.
3. **Fail closed.** Any doubt — hash mismatch, stale data, failed health check, unconfirmed deletion — means *do nothing and alert*.
4. **Public interfaces only.** External tools are driven through their CLIs and documented outputs (e.g. `kei manifest --format json`), never their internal databases.
5. **Verified dependencies.** Latest stable releases of official or actively maintained projects, each cited in §9.

## 3. Why build this (alternatives evaluated)

| Option | Verdict | Evidence |
|---|---|---|
| icloudpd "Move" mode | Rejected as deleter | Deletes assets whose download failed (#1312, open); >1 GB videos truncated (#1311, open); seeking a maintainer since 2026-01 (#1305) |
| kei | **Adopted as downloader** | MIT, active (v0.24.1, 2026-10-03), checksum-verified downloads, JSON manifest; iCloud deletion is an explicit non-goal (product charter) |
| pyicloud (timlaing fork) | **Adopted as deletion transport** (sidecar) | MIT, 2.7.0 (2026-09-04), CloudKit `PhotoAsset.delete()`; bug #353 (per-record errors discarded) ⇒ every delete is re-confirmed |
| PhotoSync / Immich / Ente | Not the pipeline | iOS requires a user tap to delete from the phone library; background upload is OS-scheduled |
| iCloud for Windows sync folder | Not used by homecloud | Download throttled (~1 MB/s measured); cannot relocate off C:; on-demand reads time out |
| Write our own iCloud client | Rejected | Re-implements Apple's private SRP/2FA/CloudKit surface that kei/pyicloud already maintain |

## 4. Architecture

```
            ┌────────────── homecloud (Go, per-user logon process) ──────────────┐
 iCloud ──► │ kei (pinned) ──► manifest ──► ledger (SQLite) ◄── verify tiers     │
            │                                   │                                │
            │                       policy (pure) ──► deleter ──► pyicloud sidecar ├──► iCloud
            │ health · scrub · drive mirror · backup monitor · ntfy · status UI  │
            └────────────────────────────────────────────────────────────────────┘
```

Packages:

| Package | Responsibility |
|---|---|
| `cmd/homecloud` | CLI wiring (urfave/cli v3): `run`, `status`, `sync`, `verify`, `scrub`, `report`, `delete`, `login`, `doctor`, `install`, `uninstall` |
| `internal/config` | Strict TOML load + validation |
| `internal/ledger` | SQLite (WAL), embedded migrations (`PRAGMA user_version`), queries |
| `internal/kei` | kei process wrapper: version pin, sync, manifest parsing, exit-code mapping |
| `internal/verify` | Verification tiers + scrub scheduling |
| `internal/policy` | Pure deletion-eligibility predicate and batch caps (no I/O) |
| `internal/deleter` | Journal (fsync before any call), sidecar execution, post-delete confirmation |
| `internal/health` | Volume identity, disk health, free space, ledger integrity, data freshness |
| `internal/drive` | iCloud Drive mirror |
| `internal/backup` | Local iPhone backup monitor |
| `internal/notify` | ntfy client |
| `internal/web` | Read-only status page + kill switch |
| `internal/app` | Scheduler loop + single global job lock |
| `sidecar/icloud_delete.py` | PEP 723 script (`uv run --script`), pinned `pyicloud==2.7.0` |

Process model: a **per-user process launched at logon** by a Task Scheduler user task (`homecloud install`). Not a Windows service: services run in Session 0, which cannot interact with the user's session.

Data layout (root configurable, default `E:\HomeCloud`): `Photos\` (kei-owned), `Drive\`, `kei\` (kei state), `homecloud.db`, `manifest.csv`, `deletions.jsonl`, `logs\`. Nightly `VACUUM INTO` snapshot of the ledger to `%LOCALAPPDATA%\homecloud\snapshots` (14 kept).

## 5. Ledger

- `assets(asset_id, library, version, filename, size, added_at_utc, created_at_utc, media_type, pair_key, state, state_at, last_seen_at)`
- `resources(asset_id, library, role, local_path, size, sha256, kei_sha256, tier1_at, tier2_at, tier3_at, last_scrub_at, ok)`
- `runs`, `deletion_batches`, and append-only `events` (triggers reject UPDATE/DELETE).

State machine: `discovered → downloaded → verified → eligible → delete_requested → delete_confirmed → purged_expected (+30 d)`. Any state → `quarantined` (mismatch, failed scrub, unconfirmed delete). Remote change → `changed_remote → discovered`.

## 6. Verification tiers

1. **Content:** SHA-256 of the file on E: equals kei's recorded download SHA-256; size equals the manifest size.
2. **Structure (video):** MP4/QuickTime top-level box walk — contiguous chain ending exactly at EOF, containing `moov` and `mdat`; `ftyp` first only for ISO-BMFF extensions (see §10) — catches truncation.
3. **Probe (video):** `ffprobe` reports streams and a positive duration consistent across container and streams.

Scrub: re-hash a rolling ~5 % of the archive weekly (full cycle ≈ 5 months). A scrub failure quarantines the asset and halts deletion.

## 7. Deletion

Eligibility is a pure function; **all** must hold:

- every resource of the asset (original, Live Photo video, RAW, edits) verified at its required tiers;
- `added_at_utc` older than `retention_days`;
- remote version equals archived version, and the asset was seen remotely within 24 h;
- not quarantined; match established by content hash + asset ID (never by name alone);
- health OK (expected volume GUID, disk healthy, free space ≥ threshold), ledger `integrity_check` OK, scrub clean within 7 days;
- clean-history streak ≥ configured days; enable-file present; kill-file absent;
- batch within caps (initial 50 assets / 20 GB per run, ramping monthly).

Execution: write the batch to `deletions.jsonl` (fsync) → sidecar re-fetches each asset and compares filename/size/version → deletes → re-fetches to confirm `isDeleted` → unconfirmed ⇒ quarantine + alert. Apple's *Recently Deleted* (30 days) is the final undo.

Rollout (each stage enabled explicitly by the owner): report-only (2 weeks) → dry-run (3 weeks) → canary (test album, restore drill) → capped automatic.

## 8. Phases

- **P0** — toolchain, repo, CI, this document; spikes: (A) kei `asset_id` ↔ pyicloud identity on 20 assets (gate for P3), (B) kei auth/manifest representation of Live Photos/edits/`added_at`/exit codes, (C) console-less logon launch; `homecloud doctor`.
- **P1** — kei full sync, ledger, verification, status UI, ntfy. One-time in-session reconciliation of the interim archive, then its removal.
- **P2** — iCloud Drive mirror; local encrypted iPhone backup + monitor.
- **P3** — staged deleter.

## 9. Dependencies (verified 2026-10-05)

| Dependency | Version | Source |
|---|---|---|
| Go | 1.27.1 | https://go.dev/doc/devel/release |
| modernc.org/sqlite | v1.60.1 | https://pkg.go.dev/modernc.org/sqlite?tab=versions |
| github.com/urfave/cli/v3 | v3.14.0 | https://pkg.go.dev/github.com/urfave/cli/v3?tab=versions |
| github.com/pelletier/go-toml/v2 | v2.4.3 | https://pkg.go.dev/github.com/pelletier/go-toml/v2?tab=versions |
| github.com/DeRuina/timberjack | v1.4.8 | https://pkg.go.dev/github.com/DeRuina/timberjack?tab=versions |
| howett.net/plist | v1.0.1 | https://pkg.go.dev/howett.net/plist |
| golang.org/x/sys | v0.48.0 | https://pkg.go.dev/golang.org/x/sys?tab=versions (monitor GO-2026-5024) |
| golang.org/x/text | latest | https://pkg.go.dev/golang.org/x/text |
| github.com/google/go-cmp | v0.7.0 | https://pkg.go.dev/github.com/google/go-cmp?tab=versions |
| golangci-lint | v2.14.0 | https://github.com/golangci/golangci-lint/releases |
| FFmpeg (ffprobe) | 9.0.2 (Gyan build) | https://www.gyan.dev/ffmpeg/builds/ |
| kei | 0.24.1 | https://github.com/rhoopr/kei/releases/tag/v0.24.1 |
| pyicloud | 2.7.0 | https://pypi.org/project/pyicloud/ |
| uv | 0.12.9 | https://github.com/astral-sh/uv |
| ntfy | 2.28.0 (server) | https://github.com/binwiederhier/ntfy/releases |

## 10. Spike findings

### kei configuration semantics (verified against kei v0.24.1 docs and source)

- **One copy per asset:** `[filters] albums = ["none"]`, `unfiled = true`. The unfiled pass excludes only members of *selected* albums (`compute_unfiled_exclude_ids` in `src/commands/service.rs` returns an empty set when no album is selected), so it enumerates the whole library. Album passes would write one extra copy per album membership (wiki *Folder Structure*).
- **Completeness for deletion:** `photos.edited = true` and `photos.alternative = true` so edits and RAW siblings are archived before an asset can ever be deleted; `live_photo_mode = "both"`.
- **Filenames:** `keep_unicode_in_filenames = true` (lossless names); `file_match_policy = "name-id7"` (asset-ID-qualified names, no size-based collision heuristics).
- **State location:** `data_dir` defaults to the config file's parent; homecloud places kei's config at `<root>\kei\config.toml` so kei state lives beside the archive.
- **Download integrity (kei-side):** bytes received must equal both `Content-Length` and the API `size`, otherwise retry (wiki *Download Pipeline*); SHA-256 recorded at download time (`verify --checksums`). Apple's `fileChecksum` is an MMCS signature, not a content hash.
- **Auth:** SRP + trusted-device 2FA; credentials via Windows Credential Manager (`kei password set`); headless runs exit **3** when 2FA is needed; `kei login get-code` / `submit-code` for non-interactive completion.

### Spike B results (2026-10-05, real account, 300 most recent assets)

- `kei list libraries` → one library, `PrimarySync` (primary). No shared library.
- **Manifest grain:** one row per `(asset_id, version)`. Observed versions: `original`, `adjusted` (edits, photo and video). Rows carry `download_checksum` = `local_checksum` (lowercase hex SHA-256), exact `size_bytes`, RFC 3339 UTC `created_at` / `added_at` / `downloaded_at` / `last_seen_at`, `media_type` (`photo`/`video`), `status`, `albums`.
- **Independent verification:** 310/310 files re-hashed by homecloud tooling matched kei's SHA-256 and size.
- **Throughput:** 27.3 GiB in 7 m 48 s (~60 MB/s) — vs ~1 MB/s through iCloud for Windows.
- **Write-guard:** one download was rejected with "`.kei-tmp` changed identity" (kei refuses to publish a temp file whose identity changed mid-write; Defender real-time scanning is active on this host — cause unconfirmed) and succeeded on kei's per-asset retry. homecloud must therefore treat kei's per-run failure count as advisory and the manifest + its own hashing as the source of truth.
- **Live Photos** (kei `src/state/types.rs` + 1,814 live manifest rows): one `asset_id` carries the still as `version=original, media_type=live_photo_image` and the motion clip as `version=live_original, media_type=live_photo_video`; edited Live Photos add `adjusted` + `live_adjusted`. Version keys: `original, medium, thumb, adjusted, alternative, live_original, live_medium, live_thumb, live_adjusted`. Statuses: `pending, policy_excluded, downloaded, failed`. Unknown values are rejected (fail closed).
- **Ordering:** kei enumerates newest-first; pyicloud's `photos.all` yields oldest-first (first 400 = 2022-06..08). Identity comparison (Spike A) therefore runs after the full sync covers both ranges.

### Spike C — background launch (decided 2026-10-05)

- Two entrypoints over the same `internal/` packages: `cmd/homecloud` (console CLI) and `cmd/homecloud-agent` (built with `-ldflags=-H=windowsgui`, so Windows never allocates a console; logs go to file).
- Started by a Task Scheduler **logon trigger** for the owner, interactive token: the agent runs in the user's session, where kei's credentials live (Windows Credential Manager). A named mutex enforces a single instance.
- Rejected: `conhost.exe --headless` (undocumented; flagged by EDR rules as defense evasion — Elastic "Proxy Execution via Console Window Host"); a Windows service (Session 0, LocalSystem — no access to the user's credential vault); kei's own `kei install` service mode (same reason, and homecloud must own scheduling to sequence sync → ingest → verify).

### Verification design corrections

- QuickTime `.mov` files may legitimately begin with `wide`, `mdat`, `moov`, `free`, `skip` or `pnot` rather than `ftyp` (wiki *Content Validation*). Tier 2 therefore validates a **contiguous top-level box chain ending exactly at EOF that contains `moov` and `mdat`**, and requires `ftyp` only for ISO-BMFF extensions (`.mp4`, `.m4v`, `.heic`).
- Tier 2 reads only top-level box headers (ISO/IEC 14496-12 §4.2: 32-bit size + type; size 1 ⇒ 64-bit largesize; size 0 ⇒ to EOF). That is ~60 lines of stdlib code, so the planned `abema/go-mp4` dependency was dropped.
- Tier 3 (`ffprobe -v error -show_entries format=duration:stream=codec_type,duration -of json`) requires exit 0, empty stderr, positive container duration and a video stream with positive duration. Live Photo clips carry extra `data` streams, which are ignored. Verified: a real iPhone MOV passes tiers 2–3; a copy truncated to 50 % fails both (ffprobe: "moov atom not found").
