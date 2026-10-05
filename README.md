# homecloud

Personal, private service that makes a local drive the verified home of my iCloud data
(Photos, Drive, iPhone backups) and keeps iCloud itself down to the most recent 30 days.

- Design, decisions and sources: [`docs/design.md`](docs/design.md)
- Status: **P0 — foundation** (no deletion capability exists yet)

## Requirements

- Windows 11 amd64, Go 1.27.1
- [kei](https://github.com/rhoopr/kei) 0.24.1, FFmpeg 9.0.2 (`ffprobe`), [uv](https://github.com/astral-sh/uv)

## Development

```powershell
go vet ./...
go test -race ./...
golangci-lint run
```
