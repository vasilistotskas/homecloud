package platform

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Storage IOCTLs and structure layouts from winioctl.h. Every query below
// works on handles opened with no access rights, so no elevation is needed.
const (
	ioctlStorageGetDeviceNumber uint32 = 0x2D1080
	ioctlStorageQueryProperty   uint32 = 0x2D1400

	storageDeviceProperty                 = 0  // STORAGE_PROPERTY_ID StorageDeviceProperty
	storageDeviceProtocolSpecificProperty = 50 // STORAGE_PROPERTY_ID StorageDeviceProtocolSpecificProperty
	propertyStandardQuery                 = 0

	busTypeNvme = 17 // STORAGE_BUS_TYPE BusTypeNvme

	protocolTypeNvme      = 3 // STORAGE_PROTOCOL_TYPE ProtocolTypeNvme
	nvmeDataTypeLogPage   = 2 // STORAGE_PROTOCOL_NVME_DATA_TYPE NVMeDataTypeLogPage
	nvmeLogPageHealthInfo = 2 // NVMe Get Log Page identifier: SMART / Health Information

	propertyQueryHeader      = 8  // offsetof(STORAGE_PROPERTY_QUERY, AdditionalParameters)
	protocolSpecificDataSize = 40 // sizeof(STORAGE_PROTOCOL_SPECIFIC_DATA)
	protocolDescriptorHeader = 8  // offsetof(STORAGE_PROTOCOL_DATA_DESCRIPTOR, ProtocolSpecificData)
	nvmeHealthLogSize        = 512

	deviceDescriptorBufSize = 1024
)

// DriveHealthOf reads the SMART / Health log of the NVMe drive holding path.
// Other bus types return ErrHealthUnsupported: reading ATA SMART data on
// Windows requires administrator rights.
func DriveHealthOf(_ context.Context, path string) (DriveHealth, error) {
	number, err := diskNumber(path)
	if err != nil {
		return DriveHealth{}, err
	}
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`\\.\PhysicalDrive%d`, number))
	if err != nil {
		return DriveHealth{}, fmt.Errorf("encode disk path: %w", err)
	}
	disk, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return DriveHealth{}, fmt.Errorf("open disk %d: %w", number, err)
	}
	defer windows.CloseHandle(disk) //nolint:errcheck // read-only query handle

	model, bus, err := deviceDescriptor(disk)
	if err != nil {
		return DriveHealth{}, err
	}
	if bus != busTypeNvme {
		return DriveHealth{}, fmt.Errorf("%w: disk %d (%s) is not NVMe", ErrHealthUnsupported, number, model)
	}
	log, err := nvmeHealthLog(disk)
	if err != nil {
		return DriveHealth{}, fmt.Errorf("disk %d: %w", number, err)
	}
	return parseNVMeHealthLog(model, log), nil
}

// diskNumber returns the number of the physical disk holding path's volume.
func diskNumber(path string) (uint32, error) {
	vol := filepath.VolumeName(path)
	if vol == "" {
		return 0, fmt.Errorf("%q has no volume name", path)
	}
	name, err := windows.UTF16PtrFromString(`\\.\` + strings.TrimSuffix(vol, `\`))
	if err != nil {
		return 0, fmt.Errorf("encode volume path: %w", err)
	}
	h, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("open volume %s: %w", vol, err)
	}
	defer windows.CloseHandle(h) //nolint:errcheck // read-only query handle

	// STORAGE_DEVICE_NUMBER
	var dn struct{ DeviceType, DeviceNumber, PartitionNumber uint32 }
	var n uint32
	size := uint32(unsafe.Sizeof(dn))
	if err := windows.DeviceIoControl(h, ioctlStorageGetDeviceNumber, nil, 0, (*byte)(unsafe.Pointer(&dn)), size, &n, nil); err != nil { //nolint:gosec // fixed-size Win32 struct
		return 0, fmt.Errorf("disk number of %s: %w", vol, err)
	}
	return dn.DeviceNumber, nil
}

// deviceDescriptor returns the product id and bus type from STORAGE_DEVICE_DESCRIPTOR.
func deviceDescriptor(disk windows.Handle) (string, uint32, error) {
	var query [propertyQueryHeader + 4]byte
	binary.LittleEndian.PutUint32(query[0:], storageDeviceProperty)
	binary.LittleEndian.PutUint32(query[4:], propertyStandardQuery)
	var out [deviceDescriptorBufSize]byte
	var n uint32
	if err := windows.DeviceIoControl(disk, ioctlStorageQueryProperty, &query[0], uint32(len(query)), &out[0], uint32(len(out)), &n, nil); err != nil {
		return "", 0, fmt.Errorf("query device descriptor: %w", err)
	}
	return parseDeviceDescriptor(out[:n])
}

// parseDeviceDescriptor reads STORAGE_DEVICE_DESCRIPTOR: ProductIdOffset at
// byte 16 (a NUL-terminated string, 0 when absent) and BusType at byte 28.
func parseDeviceDescriptor(b []byte) (string, uint32, error) {
	const productIDOffset, busTypeOffset = 16, 28
	if len(b) < busTypeOffset+4 {
		return "", 0, fmt.Errorf("device descriptor too short (%d bytes)", len(b))
	}
	var model string
	if off := binary.LittleEndian.Uint32(b[productIDOffset:]); off != 0 && int(off) < len(b) {
		s := b[off:]
		if end := strings.IndexByte(string(s), 0); end >= 0 {
			s = s[:end]
		}
		model = strings.TrimSpace(string(s))
	}
	return model, binary.LittleEndian.Uint32(b[busTypeOffset:]), nil
}

// nvmeHealthLog reads the NVMe SMART / Health Information log page through
// IOCTL_STORAGE_QUERY_PROPERTY (StorageDeviceProtocolSpecificProperty).
func nvmeHealthLog(disk windows.Handle) ([]byte, error) {
	var buf [propertyQueryHeader + protocolSpecificDataSize + nvmeHealthLogSize]byte
	binary.LittleEndian.PutUint32(buf[0:], storageDeviceProtocolSpecificProperty)
	binary.LittleEndian.PutUint32(buf[4:], propertyStandardQuery)
	spec := buf[propertyQueryHeader:]
	binary.LittleEndian.PutUint32(spec[0:], protocolTypeNvme)
	binary.LittleEndian.PutUint32(spec[4:], nvmeDataTypeLogPage)
	binary.LittleEndian.PutUint32(spec[8:], nvmeLogPageHealthInfo) // ProtocolDataRequestValue
	binary.LittleEndian.PutUint32(spec[16:], protocolSpecificDataSize)
	binary.LittleEndian.PutUint32(spec[20:], nvmeHealthLogSize)

	var n uint32
	if err := windows.DeviceIoControl(disk, ioctlStorageQueryProperty, &buf[0], uint32(len(buf)), &buf[0], uint32(len(buf)), &n, nil); err != nil {
		return nil, fmt.Errorf("read NVMe health log: %w", err)
	}
	return protocolData(buf[:n])
}

// protocolData extracts the returned payload from a STORAGE_PROTOCOL_DATA_DESCRIPTOR,
// using the offset and length the driver wrote back.
func protocolData(b []byte) ([]byte, error) {
	if len(b) < protocolDescriptorHeader+protocolSpecificDataSize {
		return nil, fmt.Errorf("protocol data descriptor too short (%d bytes)", len(b))
	}
	spec := b[protocolDescriptorHeader:]
	off := binary.LittleEndian.Uint32(spec[16:])
	length := binary.LittleEndian.Uint32(spec[20:])
	start := uint64(protocolDescriptorHeader) + uint64(off)
	if length < nvmeHealthLogSize || start+uint64(length) > uint64(len(b)) {
		return nil, errors.New("NVMe health log missing from the driver's reply")
	}
	return b[start : start+uint64(length)], nil
}

// parseNVMeHealthLog decodes the fields homecloud uses from the 512-byte NVMe
// SMART / Health Information log (NVMe Base Specification, log page 02h).
func parseNVMeHealthLog(model string, log []byte) DriveHealth {
	return DriveHealth{
		Model:                 model,
		Protocol:              "NVMe",
		Warnings:              nvmeWarnings(log[0]),
		TemperatureCelsius:    ptr(kelvinToCelsius(int(binary.LittleEndian.Uint16(log[1:])))),
		AvailableSparePercent: ptr(int(log[3])),
		PercentUsed:           ptr(int(log[5])),
		// Power On Hours (bytes 128–143) and Media and Data Integrity Errors
		// (bytes 160–175) are 128-bit counters; the low 64 bits cannot overflow
		// in practice.
		PowerOnHours: ptr(binary.LittleEndian.Uint64(log[128:])),
		MediaErrors:  ptr(binary.LittleEndian.Uint64(log[160:])),
	}
}

// nvmeCriticalWarnings names the bits of the NVMe SMART / Health log's
// Critical Warning byte (NVMe Base Specification, Get Log Page 02h), using the
// names udisks reports for the same bits on Linux.
var nvmeCriticalWarnings = []string{"spare", "temperature", "degraded", "readonly", "volatile_mem", "pmr_readonly"}

func nvmeWarnings(b byte) []string {
	var out []string
	for i, name := range nvmeCriticalWarnings {
		if b&(1<<i) != 0 {
			out = append(out, name)
		}
	}
	return out
}
