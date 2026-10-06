# homecloud — design

Status: **P1 done; local API, web app, desktop app and drive health live (M1–M4). No deletion capability exists yet.** · Last reviewed: 2026-10-05

Sections 4–7 mix what is built with what is planned; each planned part is marked *(planned)*.

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

The component and cycle diagrams are in the [README](../README.md#how-it-works) (Mermaid, kept
current with the code). Planned parts not drawn there: the P3 deleter (policy → journal →
pyicloud sidecar → iCloud), the P2 iCloud Drive mirror and iPhone backup monitor.

Packages:

| Package | Responsibility |
|---|---|
| `cmd/homecloud` | CLI wiring (urfave/cli v3): `doctor`, `verify`, `status`, `quarantine list` / `quarantine release`, `open`, `notify-test`, `install`, `uninstall`. *(planned: `report`, `delete`)* |
| `cmd/homecloud-agent` | The headless agent (scheduler loop); see Spike C |
| `internal/config` | Strict TOML load + validation |
| `internal/ledger` | SQLite (WAL), embedded migrations (`PRAGMA user_version`), queries, checked snapshots (`VACUUM INTO` + `integrity_check`) |
| `internal/kei` | kei process wrapper: version pin, sync, manifest parsing, exit-code mapping |
| `internal/verify` | Verification tiers (`RequiredTiers`, content, box structure, ffprobe). |
| `internal/doctor` | Read-only environment checks behind `homecloud doctor` |
| `internal/tool` | Version checks of pinned external executables |
| `internal/platform` | Every OS-specific behaviour (see §8a), including `Command`, the only way homecloud starts a program: on Windows it sets `CREATE_NO_WINDOW`, because the agent is a GUI-subsystem process and Windows would otherwise open a console window for every kei/ffmpeg/ffprobe run (enforced by a `forbidigo` rule on `os/exec`) |
| `internal/policy` | *(planned, P3)* Pure deletion-eligibility predicate and batch caps (no I/O) |
| `internal/deleter` | *(planned, P3)* Journal (fsync before any call), sidecar execution, post-delete confirmation |
| `internal/health` | Archive-drive health verdict (§8b). *(planned: volume identity, free space, ledger integrity and data freshness as deletion gates)* |
| `internal/drive` | *(planned, P2)* iCloud Drive mirror |
| `internal/backup` | *(planned, P2)* Local iPhone backup monitor |
| `internal/notify` | ntfy client |
| `internal/export` | `manifest.csv` and `README.txt` at the archive root, rewritten atomically after every cycle |
| `internal/thumbs` | Thumbnail and preview rendering and cache (§8c) |
| `internal/media` | Signed, expiring `/media` URLs and their handler (§8c) |
| `internal/api` | `homecloud.agent.v1.AgentService` implementation (Connect) |
| `internal/auth` | Bearer token + discovery file, browser login codes and sessions |
| `internal/web` | Loopback HTTP server: Host/Origin checks, CORS for the desktop app, authentication, embedded web app |
| `ui/` | Web app: React 19 + Fluent UI v9 + TanStack Router (hash) + Connect-Query, built with Vite, embedded via `ui/embed.go` |
| `internal/app` | Scheduler loop: a cycle, then whichever maintenance job is due (§8d); the ledger-writer lock it shares with the CLI |
| `sidecar/icloud_delete.py` | *(planned, P3)* PEP 723 script (`uv run --script`), pinned `pyicloud==2.7.0` |

Process model: a **per-user process launched at logon** by a Task Scheduler user task (`homecloud install`). Not a Windows service: services run in Session 0, which cannot interact with the user's session.

Data layout (root is the required `storage.root`, e.g. `E:\HomeCloud`; no default): `Photos\` (kei-owned; `Photos\Hidden\` for photos hidden in iCloud), `kei\` (the generated kei config and its sync report; kei's own state lives in `%USERPROFILE%\.config\kei`, §10), `homecloud.db`, `manifest.csv`, `README.txt`, `.cache\thumbnails\` (hidden, derived) and `logs\` (Windows only). Ledger snapshots go to `backup.dir` on another drive (§6). *(planned: `Drive\`, `deletions.jsonl`.)*

## 5. Ledger

Schema as of migration 0002 (`internal/ledger/migrations`; STRICT tables, RFC 3339 UTC text timestamps):

- `assets(library, asset_id, state, state_changed_at, created_at, added_at, last_seen_at)`
- `resources(library, asset_id, version, filename, local_path, media_type, size_bytes, kei_sha256, verified_content_at, verified_structure_at, verified_probe_at, last_scrub_at, failure, requires_structure, requires_probe)` — one row per rendition (kei manifest grain)
- `runs`, and append-only `events` (triggers reject UPDATE/DELETE). *(planned, P3: `deletion_batches`)*

State machine (implemented): `discovered → downloaded → verified`; any state → `quarantined` (mismatch); `quarantine release` returns an asset with an audited reason. A file kei replaces or moves keeps its recorded failure, so a quarantine lasts until it is released. *(planned, P3: `→ eligible → delete_requested → delete_confirmed → purged_expected (+30 d)`; quarantine on an unconfirmed delete; remote change → `changed_remote → discovered`.)*

## 6. Verification tiers

1. **Content:** SHA-256 of the file on E: equals kei's recorded download SHA-256; size equals the manifest size. A missing file is a mismatch (the expected bytes are not there); every cycle first confirms the archive volume is mounted, so a missing drive is never mistaken for missing files.
2. **Structure (`.mov`/`.mp4`/`.m4v` video only, per `verify.RequiredTiers`):** MP4/QuickTime top-level box walk — contiguous chain ending exactly at EOF, containing `moov` and `mdat`; `ftyp` first only for ISO-BMFF extensions (see §10) — catches truncation.
3. **Probe (video):** `ffprobe` reports streams and a positive duration consistent across container and streams. Only ffprobe exiting non-zero counts against the file; failing to start ffprobe is retried, never quarantined. Each run is bounded (5 min).

Scrub (implemented): after a cycle, when `schedule.scrub_interval` has passed since the last *completed* scrub, the agent re-hashes `schedule.scrub_fraction` of verified renditions (rounded up, at least one), those whose content was confirmed longest ago first (`resources.last_scrub_at`: set by every content pass, cleared when the file changes or fails; exported as `verified_at` in `manifest.csv`). With 5 % weekly the whole archive is re-read every 20 weeks. A changed **or missing** file is a mismatch: the content tier's pass is cleared, the asset is quarantined (so P3 never deletes it from iCloud) and an urgent alert is sent. Missing files only count when the configured archive volume is mounted; otherwise the run fails without touching anything and is retried at the next cycle. Unreadable files are retried first next run. Each scrub is a `scrub` run with a `ScrubReport`; a run that could not do its work records an `error` in its report, and only runs without one count for the schedule.

Ledger backup (implemented): every `backup.interval` the agent writes `homecloud-<UTC time>.db` to `backup.dir` with `VACUUM INTO` (one consistent view of the live ledger), runs `PRAGMA integrity_check` on the copy, and only then renames it into place; the newest `backup.keep` snapshots are kept and nothing else in the directory is touched. Partial snapshots left by an interrupted backup are pruned too. Doctor requires `backup.dir` to be on a different volume than `storage.root`. A failure is retried every cycle and alerts at most once a day. Each backup is a `ledger_backup` run. Restoring: stop the agent, delete `homecloud.db-wal` and `homecloud.db-shm` (a stale write-ahead log would be replayed onto the restored file), then copy a snapshot over `<root>/homecloud.db`.

## 7. Deletion *(planned, P3; nothing below is implemented)*

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
- **P2** — local encrypted iPhone backup + monitor (milestone A, implemented — §8e); homecloud's own iCloud sign-in (milestone B1, implemented — §8f); iCloud Drive archive (milestone B2, implemented — §8g) and its app views (B3, implemented: Overview card and sign-in banner, Activity "iCloud Drive" tab and events, Settings group; the API's Overview carries Drive totals and the session's sign-in time, never the session itself).
- **P3** — staged deleter.

## 8a. Platforms, API and GUI (decided 2026-10-05)

- **Platforms:** Windows and Linux are both first-class for the agent and CLI. OS-specific behaviour lives only in `internal/platform` (`*_windows.go` / `*_linux.go`); business logic never branches on `runtime.GOOS`. Unsupported operating systems fail to compile rather than run degraded.

  | Concern | Windows | Linux |
  |---|---|---|
  | Archive volume identity (`storage.volume_id`) | volume GUID name `\\?\Volume{…}\` | filesystem UUID: `statx(STATX_MNT_ID)` → the mount's `major:minor` in `/proc/self/mountinfo` → the `/dev/disk/by-uuid` entry whose device number matches (so a root mounted as `/dev/root` resolves); filesystems with an anonymous device number (major 0, e.g. btrfs) match by their source device. No root needed |
  | Free space | `GetDiskFreeSpaceEx` | `statfs` `Bavail × Frsize` |
  | Single instance | `Local\` named mutex | `flock(LOCK_EX\|LOCK_NB)` on `$XDG_RUNTIME_DIR/homecloud/agent.lock` |
  | Start at logon | Task Scheduler logon task (`schtasks /XML`) | systemd `--user` unit, `Type=notify`, `Restart=on-failure` (linger for headless hosts) |
  | Agent logs | rotating files under `storage.root\logs` | stderr → journald |
  | Windowless build | `-ldflags=-H=windowsgui` | n/a |

  kei's Linux binaries require glibc ≥ 2.39 (Ubuntu 24.04 / Debian 13+).
- **Process model:** the agent is always a separate, headless process that owns the ledger, kei, verification, notifications and (later) deletion. No GUI ever hosts it.
- **Local API (implemented, M1):** Connect-RPC (connect-go v1.21.0; v2 is only a release candidate), contract `proto/homecloud/agent/v1/agent.proto`, Buf v2 config with local plugins pinned as Go `tool` dependencies; generated Go code committed under `gen/`; CI runs `buf lint`, a generated-code drift check and `buf breaking` (PR base, or the previous commit on push).
  - Transport: plain HTTP/1.1 on the loopback `web.listen` (Connect unary + server streaming work over HTTP/1.1; browsers never use h2c). No server-wide write timeout because `WatchOverview` streams; read timeouts stay.
  - Request pipeline: exact Host allow-list (`web.listen`, `localhost:port`) → Origin allow-list (own origins + the Tauri webview origins `http://tauri.localhost` on Windows and `tauri://localhost` on Linux) → CORS (rs/cors, desktop origins only, `Authorization` added to connect-cors' header lists) → security headers → authentication.
  - Credentials: a 256-bit bearer token generated at every agent start and published in `agent.json` (per-user state dir) — created on Windows with a protected owner-only DACL `D:P(A;;FA;;;<user SID>)` via `CreateFile` (Go's file mode bits do not set ACLs on Windows), mode 0600 on Linux, written atomically. Browsers get an in-memory session cookie (`HttpOnly`, `SameSite=Strict`, 7 days) by exchanging a single-use 8-character login code (2 min, issued only to bearer holders; 5 wrong guesses revoke all codes); cookie calls also pass `http.CrossOriginProtection`. No `Secure` attribute: plain HTTP on loopback.
  - Versioning: an incompatible API change gets a new proto package (`homecloud.agent.v2`), which `buf breaking` enforces, so an outdated client fails with `Unimplemented`; `GetInfo`'s `api_version` is informational. The agent and the desktop app ship from the same commit. Destructive actions (P3) will require a per-request confirmation nonce recorded in the ledger.
  - `web.listen` must be `127.0.0.1:<port>`: it is the address the desktop app's CSP allows. A missing archive drive is answered with `FailedPrecondition`, which the app shows as such instead of "agent unreachable"; only transient errors (`Unavailable`, `DeadlineExceeded`, `Aborted`, `ResourceExhausted`) are retried by the app.
  - Rejected: Tauri `plugin-http` (Rust-side fetch; server-streaming support undocumented), cookie-only auth inside Tauri (third-party cookie context), h2c (no browser support, not needed).
- **GUI (implemented, M2–M3):** one React 19 + Fluent UI v9 single-page app (`ui/`), hosted (a) by the agent itself (embedded with `go:embed`) for browsers and Linux/headless hosts, and (b) in a Tauri 2.12 desktop shell (`desktop/`). The design reference is the canvas linked from the plan; Fluent's `webLightTheme`/`webDarkTheme` follow the OS setting unless the user picks light or dark (Fluent ships no separate Windows 11 theme).
  - Routing is hash-based (TanStack Router) so both hosts work without server rewrites. Data: Connect-Query for unary calls; one app-wide `WatchOverview` stream with exponential backoff, which also refetches all queries after a reconnect.
  - Browser auth: `homecloud open` → one-time code in the link → `ExchangeLoginCode` → session cookie. Desktop auth: the Rust side reads `agent.json` (`agent_endpoint` command) and the web app sends the bearer token directly (native fetch + exact-origin CORS); on `Unauthenticated` (agent restarted with a new token) it re-reads the file and retries.
  - The shell grants the webview only `core:default` and three app commands: `agent_endpoint`, `start_agent` (runs the logon task / systemd unit), `reveal_in_folder` (reveal only; nothing is opened or executed). Plugins: single-instance, window-state, opener (used from Rust only), log. NSIS installer in `currentUser` mode; WebView2 via the download bootstrapper.
  - CSP is set in `tauri.conf.json` (`connect-src` limited to IPC and `http://127.0.0.1:*`). Verified on the built app: Griffel's runtime `<style>` injection works with Tauri's CSP handling, so no CSP relaxation beyond `style-src 'unsafe-inline'` is needed.
  - The agent is installed and started separately (`homecloud install`) and never supervised by the shell. Deferred: tray, updater and Mica (no need yet; the updater needs signed releases). Rejected for now: bundling the agent as a Tauri sidecar (ties its lifecycle to the GUI), the autostart plugin (the agent already starts at logon), the Tauri MCP bridge plugin for testing (WebView2's DevTools port is used instead, no app code).
  - Rejected stacks: Wails v3 (beta, single principal maintainer), WinUI 3 (Windows-only), Fyne/Gio (non-Fluent, low-level), Avalonia/Flutter/Electron (additional stack or weight), MAUI (no Linux).

## 8b. Archive drive health (implemented, M4)

The archive is the only copy (no second copy, by decision), so the agent reads the archive drive's own SMART data every cycle (`internal/platform` `DriveHealthOf`, judged by `internal/health`).

- **Windows:** `IOCTL_STORAGE_QUERY_PROPERTY` with `StorageDeviceProtocolSpecificProperty` reads the NVMe SMART / Health log (log page 02h) through a handle opened with no access rights, so it works from the non-elevated agent (verified on this PC at Medium integrity). The disk is found from the volume with `IOCTL_STORAGE_GET_DEVICE_NUMBER`; the model and bus type come from `STORAGE_DEVICE_DESCRIPTOR`. Non-NVMe drives report "unavailable": reading ATA SMART on Windows needs administrator rights. Rejected: WMI `MSFT_PhysicalDisk.HealthStatus` through `microsoft/wmi` (the library panics on COM initialisation failure and sets process-wide COM security; the native query also gives richer data), and `Get-StorageReliabilityCounter` (needs elevation).
- **Linux:** udisks2 over D-Bus (`godbus/dbus/v5`): `Manager.ResolveDevice` with the archive's filesystem UUID (`uuid`, udisks ≥ 2.7.3, matched against `Block.IdUUID`) finds its block device and `Drive`; NVMe drives use `NVMe.Controller` (udisks ≥ 2.10) properties plus `SmartGetAttributes`, ATA drives `Drive.Ata`. Reading needs no polkit authorization; udisks refreshes SMART every 10 minutes. Interfaces are detected by introspection.
- **Verdict** (only thresholds the standards define): any critical warning (NVMe critical-warning bits, ATA failure prediction) = failing; media/data-integrity errors or NVMe "percentage used" ≥ 100 = warning; unreadable = unavailable, never treated as healthy. A state change is appended once to the event log (`drive_health_changed`) and warning/failing sends an urgent ntfy alert. P3's deletion gates will require state OK.

## 8c. Library browser (implemented, M4)

- **Thumbnails** (`internal/thumbs`): the pinned FFmpeg build (`[ffmpeg]` in the config pins `ffprobe` and `ffmpeg` together) renders WebP: a 320 px square for the grid, a fit within 1600 px for the detail view (never enlarged). Verified on the archive (ffmpeg 9.0.2): tiled iPhone HEIC must be stitched from stream group 0 (`-filter_complex [0:g:0]…`, detected with `ffprobe -show_entries stream_group=index`), everything else uses the first frame; ffmpeg applies HEIF/container rotation and JPEG EXIF orientation itself (a −90° JPEG rendered portrait). Camera raws are rendered from their largest embedded JPEG preview, fed to ffmpeg on stdin (`-f jpeg_pipe`): ffmpeg cannot develop raw sensor data, and its TIFF decoder renders the JPEG-compressed preview IFD of an Apple ProRAW DNG (DNG 1.6: IFD0 = 4032×3024 baseline JPEG, raw in a SubIFD) as a blank frame. `github.com/evanoberholster/imagemeta` sniffs the file, recognises a DNG by its `DNGVersion` tag, and extracts the preview; the preview's own EXIF orientation is applied by ffmpeg. Ordinary TIFFs and every other format still go to ffmpeg directly. 50–160 ms per image. Files are named by the source's SHA-256, so kei moving a file keeps its thumbnail. The cache lives in `<root>/.cache/thumbnails` (hidden), is derived data, and is never consulted for verification. Each size's directory is named after a fingerprint of the ffmpeg arguments that shape it (`thumb-<8 hex>`), so changing a filter, codec or quality re-renders instead of serving stale images; at startup the agent deletes every other entry of the cache directory (`Renderer.Prune`). Each cycle ends with a "preparing previews" phase rendering missing grid thumbnails (4 ffmpeg processes at most); failures are logged, never fatal.
- **Media URLs** (`internal/media`): `GET /media/{size}/{library}/{asset}/{version}?exp=…&sig=…` with an HMAC-SHA256 over path and expiry, keyed per agent start. Image elements cannot send the bearer token, and cookie-free desktop requests need no session; only ledger entries can be addressed, never arbitrary paths. Expiry is rounded to 6-hour windows (valid ≥ 12 h) so repeated listings return identical URLs and browsers cache the images.
- **API/UI**: `ListAssets` pages assets newest first (keyset on capture time and asset id), filtered by kind and by Hidden (assets under `Photos/Hidden`), showing the edited version when there is one and the picture of a Live Photo. The web app groups by month, loads more on scroll, and shows the preview, renditions and path; the desktop app adds "Show in folder" and allows `http://127.0.0.1:*` images in its CSP.

## 8d. Agent cycle and maintenance (implemented)

- **Preflight, every cycle:** the configured archive volume must be mounted at `storage.root`; otherwise the cycle stops before kei, ingest or verification runs, records a failed run (`archive_unavailable`) and alerts. Below `storage.min_free_percent` the download is skipped (`skipped_low_space`) while ingest and verification still run. Repeated conditions (sign-in needed, drive missing, low space, backup failure) alert at most once a day.
- **Maintenance:** after each cycle the agent runs the scrub and the ledger backup when no *completed* run of that kind falls within its interval (`runs.detail` carries `error` only when a run could not do its work).
- **Interrupted runs:** on Windows the agent is a GUI-subsystem process that receives no console events, so logoff or shutdown ends it without notice. Every write is crash-safe (SQLite WAL, atomic renames, kei's own journal), and at startup the agent closes every run left unfinished as failed with `interrupted`. Only one agent runs at a time, so an open run at startup was interrupted.
- **One ledger writer:** the agent holds the `homecloud-ledger-writer` lock for its whole life; `homecloud verify` and `homecloud quarantine release` take the same lock and refuse while the agent runs (the app offers both actions).
- **Live status:** verification and scrub wake the status hub after every file; each overview stream sends at most one update per second, so live counts never turn into ledger load. A sync requested during maintenance runs right after it.
- **Child processes:** `platform.Command` starts every program (kei, ffprobe, ffmpeg, system tools) with its output captured and, on Windows, `CREATE_NO_WINDOW`. Per-file tools are bounded (ffprobe 5 min, a render 2 min); a shared thumbnail render is detached from the request that started it.

## 8e. iPhone backup monitor (implemented, P2 milestone A)

- **Who makes the backups:** Apple's Devices app (Windows; desktop iTunes and libimobiledevice's `idevicebackup2` write the same layout) with "Encrypt local backup" on. The password stays with the owner; homecloud never asks for it, never decrypts `Manifest.db`, and never writes into a backup folder. Backups stay where Apple's tool puts them (owner decision 2026-10-06); homecloud only monitors.
- **What is read (`internal/iphonebackup`):** per device folder `<UDID>`: `Info.plist` (XML: `Device Name`, `Product Version`, `Unique Identifier`), `Manifest.plist` (binary: `IsEncrypted`), `Status.plist` (binary: `SnapshotState`, `Date`), via howett.net/plist. A `Snapshot` subfolder means a backup is being written; the first backup of a device may lack `Manifest.plist`/`Status.plist` until then. Folders `<UDID>-yyyymmdd-hhmmss` are archived copies: counted, not judged. Key names and types: libimobiledevice `tools/idevicebackup2.c` and The Apple Wiki "ITunes Backup".
- **Verdict per device, most severe first:** invalid (plists missing/malformed, folder ≠ UDID) › unreadable (a file could not be read in full; every file is read once per check) › unencrypted › incomplete (`SnapshotState` ≠ `finished` with no backup running) › stale (finished longer ago than `max_age`) › shrunk (more than `max_shrink_percent` smaller than the device's last healthy check) › in progress › ok.
- **Record and alerts:** a maintenance job (run kind `iphone_backup`, every `check_interval`) records an `IPhoneBackupReport`; each device's state change appends `iphone_backup_changed` (subject = UDID; recorded only on change, like drive health). Problems and "no backup on this computer" alert once a day per condition; alerts never name the device or its identifiers.
- **Owner steps:** install Apple Devices, connect the iPhone, tick "Encrypt local backup", back up once; then keep it connected or on the same Wi-Fi regularly. Until the first backup the app shows "No iPhone backup on this computer yet".

## 8f. iCloud sign-in for iCloud Drive (implemented, P2 milestone B1)

- **Why our own client:** rclone's `iclouddrive` backend deletes valid Pages/Numbers/Keynote downloads (package ZIP smaller than the listed size; rclone #8404, #9798), exposes no hashes and cannot express "archive, never delete"; kei's session is private to kei (and two clients would race rotating the same tokens). `internal/icloud` signs in as iCloud.com does; `internal/drive` reads Drive.
- **Sign-in (`homecloud icloud login`, owner only, in a terminal):** SRP-6a (`/signin/init`, `/signin/complete`; RFC 5054 2048-bit group, SHA-256, PBKDF2 `s2k`/`s2k_fo`, Apple's conventions: no account name in x, lowercase account name in M1, A and S padded, B as received) → on 409 a code: `PUT /verify/trusteddevice/securitycode` pushes it (no longer automatic since iOS 26.4), or `sms N` texts a trusted number read from `GET /appleauth/auth` (JSON or the page's `boot_args`); a valid code may be answered 409 with `securityCode.valid` or a new `X-Apple-Session-Token` → **escrow** when a 409 carries `X-Apple-EDP`/`X-Apple-PDP`: a second SRP proof with an empty account name (`/escrow/init`, `/escrow/complete` with K) → `GET /2sv/trust` (trust token) → `setup/ws/1/accountLogin` (web cookies + service map) → under Advanced Data Protection, `requestPCS` for `iclouddrive`, approved on a trusted device. A later sign-in that presents the trust token needs only the password (and escrow), no code.
- **Sources:** request sequence and headers as icloud-photos-sync PR #1124 (merged 2026-10-05, live-tested incl. escrow; GPL-3.0, used as protocol reference only); SRP adapted from rclone (MIT, see `THIRD_PARTY_NOTICES.md`) and checked against icloud-photos-sync's reference vectors (two real iCloud exchanges, `s2k_fo`, leading-zero padding of A, B and S — all match). Client id = iCloud.com's public web client id (same in both).
- **Session:** `icloud-session.json` in the per-user state dir, written owner-only and atomically (`platform.WriteSecretFile`): cookies as received (replayed into a standard `cookiejar` with the public suffix list; Max-Age stored as absolute expiry), session token, trust token, account country, service map, signed-in time. **The password is never stored.**
- **Unattended use:** a call answered 401/421 refreshes once (`setup/ws/1/validate` with the cookies, then `accountLogin` with the session and trust tokens). When Apple refuses, or asks for a code again (`hsaChallengeRequired`), or Drive needs ADP approval again (423), the result is `ErrSignInRequired` and the owner runs `homecloud icloud login` again (B2 turns this into a Drive run outcome, an app state and a daily alert).
- **Drive listing (`homecloud drive ls`, B1 spike):** `retrieveItemDetailsInFolders` on drivews (root `FOLDER::com.apple.CloudDocs::root`); item types FOLDER, FILE, APP_LIBRARY, APP_CONTAINER; an unknown type is an error, never skipped.

## 8g. iCloud Drive archive (implemented, P2 milestone B2)

- **Layout:** `<storage.root>/Drive/iCloud Drive/…` mirrors the Files app's iCloud Drive; `Drive/App Libraries/<Name> (<zone>)/…` holds each app's iCloud folder (`retrieveAppLibraries`; names repeat across apps, so the zone is part of the folder name). `Drive/.versions/<UTC time>/<path>` keeps every copy a newer one replaced; `Drive/manifest.csv` lists every archived file (including those deleted in iCloud); `Drive/.partial` holds downloads in progress and is the only place homecloud deletes from.
- **Sync (run kind `drive_sync`, every `drive.sync_interval`, after the cycle):** preflight as the cycle (archive volume, free space) → a full walk in **scopes**: the iCloud Drive tree (must list completely, or the run fails) and each app's folder, listed on its own (folders in batches of 10). `retrieveAppLibraries` also returns apps that keep no documents in iCloud Drive (Messages, Notes, Mail, banking and chat apps: drivews answers 502 for them, consistently, on this account) and apps whose folder was never created (`"status":"ID_INVALID"`, treated as empty); an app folder that cannot be listed is counted (`unlisted_apps`), and is a failure only when it holds archived files. Deletions are recorded only within scopes listed completely, so a failed or partial listing can never look like deletions → per file, by drivewsid: new → download; etag changed or copy damaged → download (same bytes again: metadata only, no version); iCloud path changed → move the copy (empty folders left behind are removed); missing from a complete walk → `deleted_in_icloud_at` recorded, copy kept; seen again → restored. Nothing at a target path is ever overwritten: it moves to `.versions` first.
- **Verification at download:** plain files must match their listed size and **Apple's content signature** from the download token: `0x01 || SHA-1("com.apple.XattrObjectSalt\0com.apple.DataObjectSalt\0" || bytes)`, confirmed against real downloads of this account (2026-10-06) — an end-to-end check against iCloud's own record. Packages (iWork and other bundles) download as a ZIP whose size never matches the listing; every entry is read so the ZIP reader checks its CRC-32. Empty files are created without a download (iCloud refuses to serve them). homecloud records its own SHA-256 of every copy.
- **Scrub:** the weekly scrub re-hashes the same share (`schedule.scrub_fraction`) of Drive files; a copy that no longer matches is marked damaged and alerted, and the next sync downloads it again, keeping the damaged copy under `.versions`.
- **Names:** each iCloud name is made safe on Windows (`<>:"/\|?*` and control characters → `_`, trailing dots/spaces kept as `_`, device names such as `CON` prefixed with `_`, over-long names shortened with a hash); two files whose local paths differ only in case get ` (<first 8 of the document id>)` before the extension. The ledger keeps each file's local path, so names never shift later.
- **Session:** the agent loads homecloud's iCloud session at each sync and saves renewed cookies afterwards only if the file is unchanged since it read it, so a `homecloud icloud login` during a sync always wins. An expired session ends the run with `sign_in_required` and alerts once a day.
- **Ledger:** migration 0003, table `icloud_drive_files` (never deleted rows); events `drive_file_archived`, `drive_file_changed` (with `kept_as`), `drive_file_moved`, `drive_file_deleted`, `drive_file_restored`, `drive_file_damaged` (library `icloud_drive`, asset id = drivewsid).
- **Advanced Data Protection:** this account uses it; Drive needs the `X-APPLE-WEBAUTH-PCS-Documents` cookie approved at sign-in, a session cookie without expiry. Its real lifetime is observed from the `drive_sync` outcomes.

## 9. Dependencies (verified 2026-10-05)

| Dependency | Version | Source |
|---|---|---|
| Go | 1.27.1 | https://go.dev/doc/devel/release |
| modernc.org/sqlite | v1.60.1 | https://pkg.go.dev/modernc.org/sqlite?tab=versions |
| github.com/urfave/cli/v3 | v3.14.0 | https://pkg.go.dev/github.com/urfave/cli/v3?tab=versions |
| github.com/pelletier/go-toml/v2 | v2.4.3 | https://pkg.go.dev/github.com/pelletier/go-toml/v2?tab=versions |
| github.com/DeRuina/timberjack | v1.4.8 | https://pkg.go.dev/github.com/DeRuina/timberjack?tab=versions |
| howett.net/plist | v1.0.2-0.20260819222228-760f9a56c6a7 (master; includes the bplist bounds fix #90, newer than v1.0.1) | https://github.com/DHowett/go-plist/commits/main |
| golang.org/x/sys | v0.48.0 | https://pkg.go.dev/golang.org/x/sys?tab=versions (monitor GO-2026-5024) |
| golang.org/x/net (publicsuffix) | v0.59.0 | https://proxy.golang.org/golang.org/x/net/@latest (2026-09-08) |
| golang.org/x/term (password prompt) | v0.46.0 | https://proxy.golang.org/golang.org/x/term/@latest (2026-09-08) |
| golang.org/x/text (indirect) | v0.41.0 | https://pkg.go.dev/golang.org/x/text |
| github.com/google/go-cmp | v0.7.0 | https://pkg.go.dev/github.com/google/go-cmp?tab=versions |
| connectrpc.com/connect (+ protoc-gen-connect-go) | v1.21.0 | https://github.com/connectrpc/connect-go/releases |
| connectrpc.com/cors | v0.1.0 | https://github.com/connectrpc/cors-go/releases |
| github.com/rs/cors | v1.11.1 | https://proxy.golang.org/github.com/rs/cors/@latest |
| google.golang.org/protobuf (+ protoc-gen-go) | v1.36.12 | https://github.com/protocolbuffers/protobuf-go/releases |
| Buf CLI (Go tool) | v1.73.0 | https://github.com/bufbuild/buf/releases |
| React / react-dom | 19.3.0 | https://www.npmjs.com/package/react |
| @fluentui/react-components / react-icons | 9.74.9 / 2.0.343 | https://www.npmjs.com/package/@fluentui/react-components |
| @connectrpc/connect(-web) / connect-query | 2.2.0 / 2.3.1 | https://github.com/connectrpc/connect-es/releases |
| @bufbuild/protobuf / protoc-gen-es | 2.16.0 | https://github.com/bufbuild/protobuf-es/releases |
| @tanstack/react-router / react-query | 1.170.41 / 5.104.1 | https://www.npmjs.com/package/@tanstack/react-router |
| Vite / Vitest | 8.3.2 / 5.0.3 | https://www.npmjs.com/package/vite |
| TypeScript (tsc) / typed lint | 7.0.2 / 6.0.x via @typescript/typescript6 | https://devblogs.microsoft.com/typescript/announcing-typescript-7-0/ |
| ESLint / typescript-eslint / Prettier | 10.12.0 / 8.71.0 / 3.9.9 | https://www.npmjs.com/package/eslint |
| pnpm / Node.js | 12.9.1 / 24 | https://github.com/pnpm/pnpm/releases |
| Tauri (crate, CLI, api) | 2.12.1 | https://github.com/tauri-apps/tauri/releases |
| Tauri plugins: single-instance / window-state / opener / log | 2.5.2 / 2.5.0 / 2.7.0 / 2.10.0 | https://crates.io/crates/tauri-plugin-opener |
| Rust | 1.98.1 | https://blog.rust-lang.org/releases/ |
| github.com/godbus/dbus/v5 | v5.2.2 | https://github.com/godbus/dbus/releases |
| github.com/evanoberholster/imagemeta | v1.1.0 | https://github.com/evanoberholster/imagemeta/releases |
| dorny/paths-filter (CI) | v4.0.3 | https://github.com/dorny/paths-filter/releases |
| FFmpeg (ffprobe + ffmpeg, one pin) | 9.0.2 (Gyan build) | https://www.gyan.dev/ffmpeg/builds/ |
| golangci-lint | v2.14.0 | https://github.com/golangci/golangci-lint/releases |
| kei | 0.24.1 | https://github.com/rhoopr/kei/releases/tag/v0.24.1 |
| pyicloud *(planned, P3 sidecar)* | 2.7.0 | https://pypi.org/project/pyicloud/ |
| uv *(planned, P3 sidecar)* | 0.12.9 | https://github.com/astral-sh/uv |
| ntfy | 2.28.0 (server) | https://github.com/binwiederhier/ntfy/releases |

## 10. Spike findings

### kei configuration semantics (verified against kei v0.24.1 docs and source)

- **One copy per asset:** `[filters] albums = ["none"]`, `unfiled = true`. The unfiled pass excludes only members of *selected* albums (`compute_unfiled_exclude_ids` in `src/commands/service.rs` returns an empty set when no album is selected), so it enumerates the whole library. Album passes would write one extra copy per album membership (wiki *Folder Structure*).
- **Completeness for deletion:** `photos.edited = true` and `photos.alternative = true` so edits and RAW siblings are archived before an asset can ever be deleted; `live_photo_mode = "both"`.
- **Filenames:** `keep_unicode_in_filenames = true` (lossless names); `file_match_policy = "name-id7"` (asset-ID-qualified names, no size-based collision heuristics).
- **State location:** kei documents `data_dir` as defaulting to the config file's parent, but on this machine `kei status` reports its state DB and session under `%USERPROFILE%\.config\kei\cookies\` (observed 2026-10-05; the config lives at `<root>\kei\config.toml`). kei's state is therefore on the system drive, not with the archive; losing it costs a re-enumeration, not files. Not changed: pointing `data_dir` elsewhere would start kei with an empty state.
- **Hidden photos are not in the library-wide pass (found 2026-10-05):** with `albums = ["none"]` + `unfiled = true`, kei's only pass is `library.all()` (iCloud's "All Photos"), which excludes Hidden items. An interrupted album/metadata refresh run downloaded 9 Hidden photos that had never been archived. With `icloud.include_hidden = true` the generated config adds the Hidden smart-folder pass (a named smart folder needs no `all-with-sensitive`), written under `Photos/Hidden/%Y/%m` and hidden on disk after each cycle (`platform.MarkHidden`: the Windows hidden attribute; on Linux an entry in `Photos/.hidden`, honoured by Nautilus, the GTK file chooser, Dolphin and Konqueror, not Thunar). It cannot be a dot-folder: kei strips leading dots from every path component (`sanitize_path_component`), which the first run showed (`PhotosHidden`).
- **homecloud generates kei's config** (`kei.RenderConfig`, written by the agent at startup with an owner-only DACL/0600 because it names the Apple Account; `homecloud doctor` reports drift). Every archive-relevant kei option is fixed in code rather than hand-edited: originals of every rendition, one `%Y/%m` tree for unfiled *and* album passes (`folder_structure_albums = "%Y/%m"`, so no pass can create album-folder duplicates), XMP sidecars, the Hidden pass. Verified by `kei config show` with kei 0.24.1.
- **Sidecars:** `[metadata] xmp_sidecar = true` writes XMP next to newly downloaded files, and every normal sync also backfills existing files in bounded batches ("Applying metadata rewrites to on-disk files count=500" in kei's log), so no repair run is needed. `kei sync --refresh-metadata` requires a config without `albums`/`smart_folders` keys, which makes kei run album passes; with the old `{album}` template that downloaded album copies and re-pointed 67 assets to them (homecloud re-verified them at the new paths; the identical date-folder twins were moved to `_tidy/`). Changing the path settings makes kei reconcile recorded paths ("Download path config changed since last sync; … staging local reconciliation").
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

### Spike A — asset identity (passed 2026-10-05) — gate for P3

Compared a read-only pyicloud 2.7.0 listing (400 oldest assets, `spike_a_ids.py`) with the full kei manifest:

- kei `asset_id` = pyicloud `PhotoAsset.asset_id` (the **CPLAsset** record name, which `PhotoAsset.delete()` targets) for **400/400**; 0 matched `master_id` (CPLMaster).
- Original size, `added_date` (to the second) and filename stem equal for **400/400**.
- pyicloud reported 252 Live Photos; kei holds both `original` and `live_original` for **252/252**.
- pyicloud 2.7.0 notes: install with the `[cli]` extra (it imports `rich` unconditionally — pyicloud #361, fixed after 2.7.0); 2FA must follow `request_2fa_code()` → `validate_2fa_code()` → `trust_session()` (as pyicloud's own CLI does), otherwise validation takes the SMS path; `delete()` returns `True` unconditionally (pyicloud #353) — deletions must be confirmed by re-fetch.

Conclusion: deletion addresses assets by kei `asset_id`, cross-checked against size, added date and filename before every delete.

### Full-library results (2026-10-05)

- kei full sync: 5,377 files downloaded, 0 failed, 239.0 GiB in 59 m 18 s.
- First full verification: 3,661 assets / 5,687 renditions. One false quarantine (an animated GIF that kei classes as video was box-walked as ISO-BMFF) led to per-container tier selection (`verify.RequiredTiers`, migration 0002) and the audited `quarantine release` command. After the fix: **3,661/3,661 verified, 0 quarantined**.

### Spike C — background launch (decided 2026-10-05)

- Two entrypoints over the same `internal/` packages: `cmd/homecloud` (console CLI) and `cmd/homecloud-agent` (built with `-ldflags=-H=windowsgui`, so Windows never allocates a console; logs go to file).
- Started by a Task Scheduler **logon trigger** for the owner, interactive token: the agent runs in the user's session, where kei's credentials live (Windows Credential Manager). A named mutex enforces a single instance.
- Rejected: `conhost.exe --headless` (undocumented; flagged by EDR rules as defense evasion — Elastic "Proxy Execution via Console Window Host"); a Windows service (Session 0, LocalSystem — no access to the user's credential vault); kei's own `kei install` service mode (same reason, and homecloud must own scheduling to sequence sync → ingest → verify).

### Verification design corrections

- QuickTime `.mov` files may legitimately begin with `wide`, `mdat`, `moov`, `free`, `skip` or `pnot` rather than `ftyp` (wiki *Content Validation*). Tier 2 therefore validates a **contiguous top-level box chain ending exactly at EOF that contains `moov` and `mdat`**, and requires `ftyp` only for ISO-BMFF extensions (`.mp4`, `.m4v`; HEIC stills are photos and get no box walk).
- Tier 2 reads only top-level box headers (ISO/IEC 14496-12 §4.2: 32-bit size + type; size 1 ⇒ 64-bit largesize; size 0 ⇒ to EOF). That is ~60 lines of stdlib code, so the planned `abema/go-mp4` dependency was dropped.
- Tier 3 (`ffprobe -v error -show_entries format=duration:stream=codec_type,duration -of json`) requires exit 0, empty stderr, positive container duration and a video stream with positive duration. Live Photo clips carry extra `data` streams, which are ignored. Verified: a real iPhone MOV passes tiers 2–3; a copy truncated to 50 % fails both (ffprobe: "moov atom not found").
