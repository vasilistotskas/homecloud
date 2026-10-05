# homecloud

Personal, private service that makes a local drive the verified home of my iCloud data
(Photos, Drive, iPhone backups) and keeps iCloud itself down to the most recent 30 days.

- Design, decisions, sources and spike findings: [`docs/design.md`](docs/design.md)
- Status: **P1 done; local API and web app live; desktop app in progress**. No deletion capability exists yet.

## How it works

`homecloud-agent` runs at logon. Every `schedule.sync_interval` it runs
[kei](https://github.com/rhoopr/kei) to download new iCloud Photos to the archive, ingests kei's
manifest into a SQLite ledger, and verifies every file (exact size + SHA-256, MP4/QuickTime box
structure, `ffprobe`). A mismatch quarantines the asset and sends an ntfy alert.

The agent serves a local API ([Connect](https://connectrpc.com), contract in
`proto/homecloud/agent/v1`) on the loopback address `web.listen`. Same-user clients read the
bearer token from the owner-only discovery file `agent.json` in the per-user state directory
(`%LOCALAPPDATA%homecloud` on Windows, `$XDG_STATE_HOME/homecloud` on Linux); the token changes
every time the agent starts.

## Requirements

- Windows 11 amd64 or Linux, Go 1.27.1, Node.js 24 with pnpm (version pinned in `ui/package.json`)
- kei 0.24.1, FFmpeg 9.0.2 (`ffprobe`), uv — exact versions are pinned in the config and checked by `homecloud doctor`

## Build and install

```powershell
# The agent embeds the web app (ui/), so build that first.
pnpm --dir ui install --frozen-lockfile
pnpm --dir ui build

$dest = "$env:LOCALAPPDATA\Programs\homecloud"
go build -trimpath -o "$dest\homecloud.exe" ./cmd/homecloud
go build -trimpath -ldflags=-H=windowsgui -o "$dest\homecloud-agent.exe" ./cmd/homecloud-agent
& "$dest\homecloud.exe" doctor
& "$dest\homecloud.exe" install     # logon task + start now
```

Configuration lives at `%APPDATA%\homecloud\config.toml` (override with `--config` or
`HOMECLOUD_CONFIG`); its schema is `internal/config`. kei's own configuration is referenced by
`kei.config`.

## Operating

| Command | Purpose |
|---|---|
| `homecloud doctor` | Read-only environment checks (volume identity, free space, pinned tool versions) |
| `homecloud verify` | Ingest kei's manifest and run all outstanding verification (no iCloud contact) |
| `homecloud open` | Open the web app in the browser, signed in through a one-time code |
| `homecloud status` | Asset counts by state |
| `homecloud notify-test` | Send a test push notification |
| `homecloud install` / `uninstall` | Register / remove the logon task |

When kei's Apple session expires the agent sends an ntfy alert; re-authenticate with
`kei login --config <kei config>` and approve the code on a trusted device.

Logs: `<storage.root>\logs\homecloud-agent.log` and `kei.log`; failures before the config is
loaded go to `%LOCALAPPDATA%\homecloud\agent-startup.log`.

## Development

```powershell
go vet ./...           # needs ui/dist (see Build)
go test ./...          # CI also runs -race
golangci-lint run
pnpm --dir ui lint; pnpm --dir ui typecheck; pnpm --dir ui test
```

- API contract: edit `proto/homecloud/agent/v1/agent.proto`, then `go tool buf lint` and
  `go tool buf generate` (needs `pnpm install` in `ui/`, which pins `protoc-gen-es`). Generated code
  under `gen/` and `ui/src/gen/` is committed; CI fails if it is stale or breaks the API.
- Web app: `pnpm --dir ui dev` serves it with hot reload and proxies the API to the running agent,
  found through its discovery file; sign in with the code printed by `homecloud open`.
