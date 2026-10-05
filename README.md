# homecloud

Personal, private service that makes a local drive the verified home of my iCloud data
(Photos, Drive, iPhone backups) and keeps iCloud itself down to the most recent 30 days.

- Design, decisions, sources and spike findings: [`docs/design.md`](docs/design.md)
- Status: **P1 — ingest, verification, agent, status page**. No deletion capability exists yet.

## How it works

`homecloud-agent` runs at logon. Every `schedule.sync_interval` it runs
[kei](https://github.com/rhoopr/kei) to download new iCloud Photos to the archive, ingests kei's
manifest into a SQLite ledger, and verifies every file (exact size + SHA-256, MP4/QuickTime box
structure, `ffprobe`). A mismatch quarantines the asset and sends an ntfy alert. A local status
page is served on `web.listen`.

## Requirements

- Windows 11 amd64, Go 1.27.1
- kei 0.24.1, FFmpeg 9.0.2 (`ffprobe`), uv — exact versions are pinned in the config and checked by `homecloud doctor`

## Build and install

```powershell
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
| `homecloud status` | Asset counts by state |
| `homecloud notify-test` | Send a test push notification |
| `homecloud install` / `uninstall` | Register / remove the logon task |

When kei's Apple session expires the agent sends an ntfy alert; re-authenticate with
`kei login --config <kei config>` and approve the code on a trusted device.

Logs: `<storage.root>\logs\homecloud-agent.log` and `kei.log`; failures before the config is
loaded go to `%LOCALAPPDATA%\homecloud\agent-startup.log`.

## Development

```powershell
go vet ./...
go test ./...          # CI also runs -race
golangci-lint run
```
