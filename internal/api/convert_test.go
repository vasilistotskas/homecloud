package api

import (
	"testing"
	"time"

	agentv1 "github.com/vasilistotskas/homecloud/gen/homecloud/agent/v1"
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
	if d.GetState() != agentv1.DriveHealthState_DRIVE_HEALTH_STATE_FAILING || d.GetTemperatureCelsius() != 55 ||
		d.GetPercentUsed() != 4 || d.GetPowerOnHours() != 18658 || d.MediaErrors != nil || d.GetModel() != "CT2000T700SSD5" {
		t.Fatalf("drive = %v", d)
	}
}

func TestDriveHealthEventToProto(t *testing.T) {
	e, err := eventToProto(ledger.Event{
		ID: 1, Kind: ledger.DriveHealthEvent, Detail: `{"from":"","to":"warning","reasons":["3 media errors"]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := e.GetDriveHealthChanged()
	if c.GetFrom() != agentv1.DriveHealthState_DRIVE_HEALTH_STATE_UNSPECIFIED ||
		c.GetTo() != agentv1.DriveHealthState_DRIVE_HEALTH_STATE_WARNING || c.GetReasons()[0] != "3 media errors" {
		t.Fatalf("event = %v", e)
	}
	if _, err := eventToProto(ledger.Event{ID: 2, Kind: ledger.DriveHealthEvent, Detail: `{"to":"bogus"}`}); err == nil {
		t.Fatal("an unknown state must be rejected")
	}
}
