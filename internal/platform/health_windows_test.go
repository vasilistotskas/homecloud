package platform

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseNVMeHealthLog(t *testing.T) {
	log := make([]byte, nvmeHealthLogSize)
	log[0] = 0b0000_0101                            // spare + degraded
	binary.LittleEndian.PutUint16(log[1:], 328)     // 55 °C
	log[3] = 97                                     // available spare
	log[5] = 4                                      // percentage used
	binary.LittleEndian.PutUint64(log[128:], 18658) // power-on hours
	binary.LittleEndian.PutUint64(log[160:], 2)     // media errors

	got := parseNVMeHealthLog("Samsung SSD 980 PRO 1TB", log)
	want := DriveHealth{
		Model: "Samsung SSD 980 PRO 1TB", Protocol: "NVMe",
		Warnings:           []string{"spare", "degraded"},
		TemperatureCelsius: ptr(55), AvailableSparePercent: ptr(97), PercentUsed: ptr(4),
		PowerOnHours: ptr(uint64(18658)), MediaErrors: ptr(uint64(2)),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("health (-want +got):\n%s", diff)
	}
}

func TestParseDeviceDescriptor(t *testing.T) {
	b := make([]byte, 64)
	binary.LittleEndian.PutUint32(b[16:], 40)
	binary.LittleEndian.PutUint32(b[28:], busTypeNvme)
	copy(b[40:], "Fast Disk  \x00junk")
	model, bus, err := parseDeviceDescriptor(b)
	if err != nil || model != "Fast Disk" || bus != busTypeNvme {
		t.Fatalf("descriptor = %q %d %v", model, bus, err)
	}
	if _, _, err := parseDeviceDescriptor(b[:10]); err == nil {
		t.Fatal("a short descriptor must be rejected")
	}
}

func TestProtocolDataBounds(t *testing.T) {
	b := make([]byte, protocolDescriptorHeader+protocolSpecificDataSize+nvmeHealthLogSize)
	spec := b[protocolDescriptorHeader:]
	binary.LittleEndian.PutUint32(spec[16:], protocolSpecificDataSize)
	binary.LittleEndian.PutUint32(spec[20:], nvmeHealthLogSize)
	if data, err := protocolData(b); err != nil || len(data) != nvmeHealthLogSize {
		t.Fatalf("protocolData = %d bytes, %v", len(data), err)
	}
	binary.LittleEndian.PutUint32(spec[20:], nvmeHealthLogSize*2)
	if _, err := protocolData(b); err == nil {
		t.Fatal("a length past the buffer must be rejected")
	}
}

// TestDriveHealthOfThisMachine reads the real drive holding the temp directory.
// CI runners use virtual disks, which are not NVMe.
func TestDriveHealthOfThisMachine(t *testing.T) {
	h, err := DriveHealthOf(context.Background(), t.TempDir())
	if errors.Is(err, ErrHealthUnsupported) {
		t.Skipf("drive health unsupported here: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if h.Protocol != "NVMe" || h.PercentUsed == nil || h.PowerOnHours == nil || h.TemperatureCelsius == nil {
		t.Fatalf("health = %+v", h)
	}
	t.Logf("%s: warnings=%v temp=%d°C used=%d%% hours=%d", h.Model, h.Warnings, *h.TemperatureCelsius, *h.PercentUsed, *h.PowerOnHours)
}
