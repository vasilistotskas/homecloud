package api

import (
	"testing"
	"time"

	agentv2 "github.com/vasilistotskas/homecloud/gen/homecloud/agent/v2"
	"github.com/vasilistotskas/homecloud/internal/app"
	"github.com/vasilistotskas/homecloud/internal/health"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

func TestDriveToProto(t *testing.T) {
	if d, err := driveToProto(health.Report{}); d != nil || err != nil {
		t.Fatalf("unchecked drive = %v, %v; want nil", d, err)
	}
	temp, used := 55, 4
	hours := uint64(18658)
	d, err := driveToProto(health.Report{
		State:     health.StateFailing,
		CheckedAt: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
		Reasons:   []string{"drive reports: spare"},
		Drive: &platform.DriveHealth{
			Model: "CT2000T700SSD5", Protocol: "NVMe", Warnings: []string{"spare"},
			TemperatureCelsius: &temp, PercentUsed: &used, PowerOnHours: &hours,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.GetState() != agentv2.DriveHealthState_DRIVE_HEALTH_STATE_FAILING || d.GetTemperatureCelsius() != 55 ||
		d.GetPercentUsed() != 4 || d.GetPowerOnHours() != 18658 || d.MediaErrors != nil || d.GetModel() != "CT2000T700SSD5" {
		t.Fatalf("drive = %v", d)
	}
}

func TestDriveHealthEventToProto(t *testing.T) {
	e, err := eventToProto(ledger.Event{
		ID: 1, Kind: ledger.EventDriveHealthChanged, Detail: `{"from":"","to":"warning","reasons":["3 media errors"]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := e.GetDriveHealthChanged()
	if c.GetFrom() != agentv2.DriveHealthState_DRIVE_HEALTH_STATE_UNSPECIFIED ||
		c.GetTo() != agentv2.DriveHealthState_DRIVE_HEALTH_STATE_WARNING || c.GetReasons()[0] != "3 media errors" {
		t.Fatalf("event = %v", e)
	}
	if _, err := eventToProto(ledger.Event{ID: 2, Kind: ledger.EventDriveHealthChanged, Detail: `{"to":"bogus"}`}); err == nil {
		t.Fatal("an unknown state must be rejected")
	}
}

func TestIPhoneBackupEventToProto(t *testing.T) {
	e, err := eventToProto(ledger.Event{
		ID: 1, Kind: ledger.EventIPhoneBackupChanged,
		Detail: `{"subject":"C:\\Backup\\UDID-A","udid":"UDID-A","device_name":"iPhone","from":"ok","to":"stale","problems":["old"]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := e.GetIphoneBackupChanged()
	if c.GetUdid() != "UDID-A" || c.GetDir() != `C:\Backup\UDID-A` || c.GetDeviceName() != "iPhone" ||
		c.GetFrom() != agentv2.IPhoneBackupState_I_PHONE_BACKUP_STATE_OK ||
		c.GetTo() != agentv2.IPhoneBackupState_I_PHONE_BACKUP_STATE_STALE || c.GetReasons()[0] != "old" {
		t.Fatalf("event = %v", e)
	}
	if _, err := eventToProto(ledger.Event{ID: 2, Kind: ledger.EventIPhoneBackupChanged, Detail: `{"to":"bogus"}`}); err == nil {
		t.Fatal("an unknown state must be rejected")
	}
}

func TestIPhoneBackupRunToProto(t *testing.T) {
	ok := true
	done := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	r, err := runToProto(ledger.Run{
		ID: 7, Kind: app.IPhoneBackupRunKind, StartedAt: done, FinishedAt: &done, OK: &ok,
		Detail: `{"devices":[{"udid":"U","name":"iPhone","encrypted":true,"completed_at":"2026-10-05T20:00:00Z","files":9,"bytes":1024,"state":"ok"}],"archived":1}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	rep := r.GetIphoneBackup()
	d := rep.GetDevices()[0]
	if rep.GetArchived() != 1 || d.GetState() != agentv2.IPhoneBackupState_I_PHONE_BACKUP_STATE_OK ||
		d.GetSizeBytes() != 1024 || !d.GetEncrypted() || d.GetCompletedAt().AsTime().Hour() != 20 {
		t.Fatalf("run = %v", r)
	}
}

func TestDriveSyncRunToProto(t *testing.T) {
	ok := false
	done := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	r, err := runToProto(ledger.Run{
		ID: 8, Kind: app.DriveSyncRunKind, StartedAt: done, FinishedAt: &done, OK: &ok,
		Detail: `{"outcome":"partial","listed":10,"archived":7,"bytes":2048,"failed":1,"failures":["a.pdf: boom"],"manifest_rows":9}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	d := r.GetDriveSync()
	if d.GetOutcome() != agentv2.DriveSyncOutcome_DRIVE_SYNC_OUTCOME_PARTIAL || d.GetArchived() != 7 ||
		d.GetDownloadedBytes() != 2048 || d.GetFailures()[0] != "a.pdf: boom" || d.GetManifestRows() != 9 {
		t.Fatalf("run = %v", r)
	}
	if _, err := runToProto(ledger.Run{ID: 9, Kind: app.DriveSyncRunKind, FinishedAt: &done, OK: &ok, Detail: `{"outcome":"bogus"}`}); err == nil {
		t.Fatal("an unknown outcome was accepted")
	}
}

func TestDriveFileMovedEventToProto(t *testing.T) {
	e, err := eventToProto(ledger.Event{
		ID: 3, Library: ledger.DriveLibrary, Asset: "FILE::z::A", Kind: ledger.EventDriveFileMoved,
		Detail: `{"from":"iCloud Drive/a.pdf","to":"iCloud Drive/Docs/b.pdf","local_path":"iCloud Drive/Docs/b.pdf"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	d := e.GetDriveFile()
	if d.GetChange() != agentv2.DriveFileChange_DRIVE_FILE_CHANGE_MOVED || d.GetFromPath() != "iCloud Drive/a.pdf" ||
		d.GetPath() != "iCloud Drive/Docs/b.pdf" || e.GetAsset().GetFilename() != "b.pdf" {
		t.Fatalf("event = %v", e)
	}
}
