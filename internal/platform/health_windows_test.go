package platform

import (
	"encoding/binary"
	"errors"
	"slices"
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

	got := parseNVMeHealthLog(log)
	want := DriveHealth{
		Protocol:           "NVMe",
		Warnings:           []string{"spare", "degraded"},
		TemperatureCelsius: new(55), AvailableSparePercent: new(97), PercentUsed: new(4),
		PowerOnHours: new(uint64(18658)), MediaErrors: new(uint64(2)),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("health (-want +got):\n%s", diff)
	}
}

func TestParseDeviceDescriptor(t *testing.T) {
	b := make([]byte, 80)
	binary.LittleEndian.PutUint32(b[16:], 40)
	binary.LittleEndian.PutUint32(b[24:], 60)
	binary.LittleEndian.PutUint32(b[28:], busTypeNvme)
	copy(b[40:], "Fast Disk  \x00junk")
	copy(b[60:], "  S5H9NS0N\x00")
	got, err := parseDeviceDescriptor(b)
	if err != nil || got != (descriptor{model: "Fast Disk", serial: "S5H9NS0N", bus: busTypeNvme}) {
		t.Fatalf("descriptor = %+v %v", got, err)
	}
	binary.LittleEndian.PutUint32(b[24:], 0xFFFFFFFF) // "no serial" as some drivers write it
	if got, err := parseDeviceDescriptor(b); err != nil || got.serial != "" {
		t.Fatalf("absent serial = %+v %v", got, err)
	}
	if _, err := parseDeviceDescriptor(b[:10]); err == nil {
		t.Fatal("a short descriptor must be rejected")
	}
}

func TestParseDiskExtents(t *testing.T) {
	reply := func(disks ...uint32) []byte {
		b := make([]byte, diskExtentsHeader+len(disks)*diskExtentSize)
		var count uint32
		for i, d := range disks {
			binary.LittleEndian.PutUint32(b[diskExtentsHeader+i*diskExtentSize:], d)
			count++
		}
		binary.LittleEndian.PutUint32(b, count)
		return b
	}
	got, err := parseDiskExtents(reply(3, 1, 3))
	if err != nil || !slices.Equal(got, []uint32{1, 3}) {
		t.Fatalf("disks = %v %v, want [1 3]", got, err)
	}
	if _, err := parseDiskExtents(reply()); err == nil {
		t.Fatal("a reply without extents must be rejected")
	}
	short := reply(1, 2)
	binary.LittleEndian.PutUint32(short, 5)
	if _, err := parseDiskExtents(short); err == nil {
		t.Fatal("a count past the reply must be rejected")
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

// TestStorageAndHealthOfThisMachine reads the real drives holding the temp
// directory. CI runners use virtual disks, which are not NVMe.
func TestStorageAndHealthOfThisMachine(t *testing.T) {
	s, err := StorageOf(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Drives) == 0 || len(s.Arrays) != 0 {
		t.Fatalf("storage = %+v, want drives and no arrays", s)
	}
	for _, d := range s.Drives {
		h, err := ReadDriveHealth(t.Context(), d)
		if errors.Is(err, ErrHealthUnsupported) {
			t.Skipf("drive health unsupported here: %v", err)
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Protocol != "NVMe" || h.Model == "" || h.PercentUsed == nil || h.PowerOnHours == nil || h.TemperatureCelsius == nil {
			t.Fatalf("health = %+v", h)
		}
		t.Logf("%s: %s warnings=%v temp=%d°C used=%d%% hours=%d", d.Name, h.Model, h.Warnings, *h.TemperatureCelsius, *h.PercentUsed, *h.PowerOnHours)
	}
}
