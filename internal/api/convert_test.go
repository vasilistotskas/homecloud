package api

import (
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	agentv3 "github.com/vasilistotskas/homecloud/gen/homecloud/agent/v3"
	"github.com/vasilistotskas/homecloud/internal/app"
	"github.com/vasilistotskas/homecloud/internal/health"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

func TestStorageHealthToProto(t *testing.T) {
	if s, err := storageHealthToProto(health.Report{}); s != nil || err != nil {
		t.Fatalf("unchecked storage = %v, %v; want nil", s, err)
	}
	s, err := storageHealthToProto(health.Report{
		State:     health.StateFailing,
		CheckedAt: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
		Reasons:   []string{"sda: drive reports: failing", "md1: sdb3 has failed"},
		Drives: []health.DriveReport{
			{Name: "sda", State: health.StateFailing, Reasons: []string{"drive reports: failing"}, Health: &platform.DriveHealth{
				Model: "ST4000VN006-3CW104", Serial: "ZW6123AB", Protocol: "ATA", Warnings: []string{"failing"},
				TemperatureCelsius: new(34), PowerOnHours: new(uint64(1200)),
			}},
			{Name: "sdb", State: health.StateUnavailable, Reasons: []string{"no device node here"}},
		},
		Arrays: []health.ArrayReport{{
			RaidArray: platform.RaidArray{
				Name: "md1", Level: "raid1", ArrayState: "clean", RaidDisks: 2, Degraded: 1, SyncAction: "idle",
				Members: []platform.RaidMember{{Name: "sda3", Drive: "sda", States: []string{"in_sync"}, Errors: 2}},
			},
			State: health.StateFailing, Reasons: []string{"sdb3 has failed"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.GetState() != agentv3.HealthState_HEALTH_STATE_FAILING || len(s.GetReasons()) != 2 || len(s.GetDrives()) != 2 {
		t.Fatalf("storage = %v", s)
	}
	sda, sdb := s.GetDrives()[0], s.GetDrives()[1]
	if sda.GetName() != "sda" || sda.GetSerial() != "ZW6123AB" || sda.GetTemperatureCelsius() != 34 || sda.GetPowerOnHours() != 1200 ||
		sda.PercentUsed != nil || sda.MediaErrors != nil || sda.GetWarnings()[0] != "failing" {
		t.Fatalf("sda = %v", sda)
	}
	if sdb.GetState() != agentv3.HealthState_HEALTH_STATE_UNAVAILABLE || sdb.GetProtocol() != "" || sdb.GetReasons()[0] != "no device node here" {
		t.Fatalf("sdb = %v", sdb)
	}
	a := s.GetArrays()[0]
	if a.GetName() != "md1" || a.GetDegraded() != 1 || a.GetRaidDisks() != 2 || a.GetState() != agentv3.HealthState_HEALTH_STATE_FAILING ||
		a.GetMembers()[0].GetDrive() != "sda" || a.GetMembers()[0].GetReadErrors() != 2 {
		t.Fatalf("array = %v", a)
	}
}

func TestAPIVersionMatchesPackage(t *testing.T) {
	pkg := string(agentv3.File_homecloud_agent_v3_agent_proto.Package())
	major, _, _ := strings.Cut(Version, ".")
	if !strings.HasSuffix(pkg, ".v"+major) {
		t.Fatalf("api.Version %s does not match the proto package %s", Version, pkg)
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
	if c.GetFrom() != agentv3.HealthState_HEALTH_STATE_UNSPECIFIED ||
		c.GetTo() != agentv3.HealthState_HEALTH_STATE_WARNING || c.GetReasons()[0] != "3 media errors" {
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
		c.GetFrom() != agentv3.IPhoneBackupState_I_PHONE_BACKUP_STATE_OK ||
		c.GetTo() != agentv3.IPhoneBackupState_I_PHONE_BACKUP_STATE_STALE || c.GetReasons()[0] != "old" {
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
	if rep.GetArchived() != 1 || d.GetState() != agentv3.IPhoneBackupState_I_PHONE_BACKUP_STATE_OK ||
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
	if d.GetOutcome() != agentv3.DriveSyncOutcome_DRIVE_SYNC_OUTCOME_PARTIAL || d.GetArchived() != 7 ||
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
	if d.GetChange() != agentv3.DriveFileChange_DRIVE_FILE_CHANGE_MOVED || d.GetFromPath() != "iCloud Drive/a.pdf" ||
		d.GetPath() != "iCloud Drive/Docs/b.pdf" || e.GetAsset().GetFilename() != "b.pdf" {
		t.Fatalf("event = %v", e)
	}
}

func TestIPhoneBackupCopyEventsToProto(t *testing.T) {
	for kind, want := range iphoneCopyChanges {
		e, err := eventToProto(ledger.Event{ID: 1, Kind: kind, Detail: `{"udid":"00008110-001A2B3C4D5E801E",` +
			`"device_name":"iPhone","stamp":"20261007T030000Z","files":7,"bytes":700,"copied_bytes":30,"reason":"retention"}`})
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		c := e.GetIphoneBackupCopy()
		if c.GetChange() != want || c.GetFiles() != 7 || c.GetCopiedBytes() != 30 || c.GetReason() != "retention" ||
			!c.GetCompletedAt().AsTime().Equal(time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)) {
			t.Fatalf("%s = %v", kind, c)
		}
	}
	// Every ledger kind can be listed through the API.
	for _, name := range []string{
		ledger.EventIPhoneBackupCopied, ledger.EventIPhoneBackupCopyRemoved,
		ledger.EventIPhoneBackupCopyDamaged, ledger.EventIPhoneBackupPushRefused,
	} {
		found := false
		for _, n := range eventKindNames {
			found = found || n == name
		}
		if !found {
			t.Errorf("%s has no API event kind", name)
		}
	}
	if _, err := eventToProto(ledger.Event{ID: 2, Kind: ledger.EventIPhoneBackupCopied, Detail: `{"stamp":"yesterday"}`}); err == nil {
		t.Fatal("a malformed stamp must be rejected")
	}
}

func TestIPhoneBackupReportCopiesToProto(t *testing.T) {
	verified := time.Date(2026, 10, 7, 4, 0, 0, 0, time.UTC)
	detail, err := json.Marshal(app.IPhoneBackupReport{
		PushErrors: []string{"backup of 20261008T030000Z: a file does not match its manifest"},
		Devices: []app.IPhoneBackupDevice{{
			UDID: "00008110-001A2B3C4D5E801E", State: "ok", FromCopy: true,
			Copies: []app.IPhoneBackupCopy{{
				CompletedAt: verified.Add(-time.Hour), Dir: "/archive/iPhone backups/x/20261007T030000Z",
				Files: 7, Bytes: 700, CopiedBytes: 30, VerifiedAt: &verified, DamagedFiles: 1,
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := iphoneBackupToProto(string(detail))
	if err != nil {
		t.Fatal(err)
	}
	d := rep.GetDevices()[0]
	c := d.GetCopies()[0]
	if !d.GetFromCopy() || len(rep.GetPushErrors()) != 1 || c.GetSizeBytes() != 700 || c.GetDamagedFiles() != 1 ||
		!c.GetVerifiedAt().AsTime().Equal(verified) {
		t.Fatalf("report = %v", rep)
	}
}
