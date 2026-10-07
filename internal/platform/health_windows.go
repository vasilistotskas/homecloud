package platform

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// Storage IOCTLs and structure layouts from winioctl.h. Every query below
// works on handles opened with no access rights, so no elevation is needed.
const (
	ioctlStorageQueryProperty uint32 = 0x2D1400

	// STORAGE_PROPERTY_ID: the disk's descriptor, and NVMe data from the
	// controller (Identify) or the device (log pages), as Microsoft's
	// "Working with NVMe drives" queries them.
	storageDeviceProperty                  = 0
	storageAdapterProtocolSpecificProperty = 49
	storageDeviceProtocolSpecificProperty  = 50
	propertyStandardQuery                  = 0

	busTypeNvme = 17 // STORAGE_BUS_TYPE BusTypeNvme

	protocolTypeNvme = 3 // STORAGE_PROTOCOL_TYPE ProtocolTypeNvme
	// STORAGE_PROTOCOL_NVME_DATA_TYPE.
	nvmeDataTypeIdentify = 1
	nvmeDataTypeLogPage  = 2
	// NVMe Base Specification: Identify CNS 01h (controller, 4096 bytes) and
	// Get Log Page 02h (SMART / Health Information, 512 bytes).
	nvmeIdentifyController = 1
	nvmeIdentifySize       = 4096
	nvmeLogPageHealthInfo  = 2
	nvmeHealthLogSize      = 512

	propertyQueryHeader      = 8  // offsetof(STORAGE_PROPERTY_QUERY, AdditionalParameters)
	protocolSpecificDataSize = 40 // sizeof(STORAGE_PROTOCOL_SPECIFIC_DATA)
	protocolDescriptorHeader = 8  // offsetof(STORAGE_PROTOCOL_DATA_DESCRIPTOR, ProtocolSpecificData)

	deviceDescriptorBufSize = 1024
)

// ReadDriveHealth reads the SMART / Health log of an NVMe drive, and its
// model and serial number from the drive's Identify Controller data. Other
// bus types return ErrHealthUnsupported: reading ATA SMART data on Windows
// requires administrator rights.
func ReadDriveHealth(_ context.Context, d Drive) (DriveHealth, error) {
	name, err := windows.UTF16PtrFromString(d.Device)
	if err != nil {
		return DriveHealth{}, fmt.Errorf("encode disk path: %w", err)
	}
	disk, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return DriveHealth{}, fmt.Errorf("open %s: %w", d.Device, err)
	}
	defer windows.CloseHandle(disk) //nolint:errcheck // read-only query handle

	model, bus, err := deviceDescriptor(disk)
	if err != nil {
		return DriveHealth{}, err
	}
	if bus != busTypeNvme {
		return DriveHealth{}, fmt.Errorf("%w: %s is not NVMe", ErrHealthUnsupported, model)
	}
	// The descriptor's serial number field holds the namespace's EUI-64 for
	// NVMe drives ("0025_385C_1190_32F6."), not the drive's serial number.
	identify, err := nvmeQuery(disk, storageAdapterProtocolSpecificProperty, nvmeDataTypeIdentify, nvmeIdentifyController, nvmeIdentifySize)
	if err != nil {
		return DriveHealth{}, fmt.Errorf("read NVMe identify data: %w", err)
	}
	log, err := nvmeQuery(disk, storageDeviceProtocolSpecificProperty, nvmeDataTypeLogPage, nvmeLogPageHealthInfo, nvmeHealthLogSize)
	if err != nil {
		return DriveHealth{}, fmt.Errorf("read NVMe health log: %w", err)
	}
	h := parseNVMeHealthLog(log)
	h.Model, h.Serial = parseIdentifyController(identify)
	return h, nil
}

// deviceDescriptor returns the product id and bus type from the disk's
// STORAGE_DEVICE_DESCRIPTOR.
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
		if end := bytes.IndexByte(s, 0); end >= 0 {
			s = s[:end]
		}
		model = strings.TrimSpace(string(s))
	}
	return model, binary.LittleEndian.Uint32(b[busTypeOffset:]), nil
}

// nvmeQuery reads NVMe data of the given size through IOCTL_STORAGE_QUERY_PROPERTY
// with a STORAGE_PROTOCOL_SPECIFIC_DATA request.
func nvmeQuery(disk windows.Handle, property, dataType, requestValue, size uint32) ([]byte, error) {
	bufLen := propertyQueryHeader + protocolSpecificDataSize + size
	buf := make([]byte, bufLen)
	binary.LittleEndian.PutUint32(buf[0:], property)
	binary.LittleEndian.PutUint32(buf[4:], propertyStandardQuery)
	spec := buf[propertyQueryHeader:]
	binary.LittleEndian.PutUint32(spec[0:], protocolTypeNvme)
	binary.LittleEndian.PutUint32(spec[4:], dataType)
	binary.LittleEndian.PutUint32(spec[8:], requestValue) // ProtocolDataRequestValue
	binary.LittleEndian.PutUint32(spec[16:], protocolSpecificDataSize)
	binary.LittleEndian.PutUint32(spec[20:], size)

	var n uint32
	if err := windows.DeviceIoControl(disk, ioctlStorageQueryProperty, &buf[0], bufLen, &buf[0], bufLen, &n, nil); err != nil {
		return nil, fmt.Errorf("IOCTL_STORAGE_QUERY_PROPERTY: %w", err)
	}
	return protocolData(buf[:n], size)
}

// protocolData extracts the returned payload from a STORAGE_PROTOCOL_DATA_DESCRIPTOR,
// using the offset and length the driver wrote back; it must hold at least size bytes.
func protocolData(b []byte, size uint32) ([]byte, error) {
	if len(b) < protocolDescriptorHeader+protocolSpecificDataSize {
		return nil, fmt.Errorf("protocol data descriptor too short (%d bytes)", len(b))
	}
	spec := b[protocolDescriptorHeader:]
	off := binary.LittleEndian.Uint32(spec[16:])
	length := binary.LittleEndian.Uint32(spec[20:])
	start := uint64(protocolDescriptorHeader) + uint64(off)
	if length < size || start+uint64(length) > uint64(len(b)) {
		return nil, fmt.Errorf("the driver returned %d of %d bytes", length, size)
	}
	return b[start : start+uint64(length)], nil
}

// parseIdentifyController reads the Serial Number (bytes 4–23) and Model
// Number (bytes 24–63), ASCII padded with spaces, from Identify Controller
// data (NVMe Base Specification, Identify CNS 01h).
func parseIdentifyController(b []byte) (model, serial string) {
	return strings.TrimSpace(string(b[24:64])), strings.TrimSpace(string(b[4:24]))
}

// parseNVMeHealthLog decodes the fields homecloud uses from the 512-byte NVMe
// SMART / Health Information log (NVMe Base Specification, log page 02h).
func parseNVMeHealthLog(log []byte) DriveHealth {
	return DriveHealth{
		Protocol:              "NVMe",
		Warnings:              NVMeWarnings(log[0]),
		TemperatureCelsius:    new(kelvinToCelsius(int(binary.LittleEndian.Uint16(log[1:])))),
		AvailableSparePercent: new(int(log[3])),
		PercentUsed:           new(int(log[5])),
		// Power On Hours (bytes 128–143) and Media and Data Integrity Errors
		// (bytes 160–175) are 128-bit counters; the low 64 bits cannot overflow
		// in practice.
		PowerOnHours: new(binary.LittleEndian.Uint64(log[128:])),
		MediaErrors:  new(binary.LittleEndian.Uint64(log[160:])),
	}
}
