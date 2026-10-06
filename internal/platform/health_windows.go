package platform

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// Storage IOCTLs and structure layouts from winioctl.h. Every query below
// works on handles opened with no access rights, so no elevation is needed.
const (
	ioctlStorageQueryProperty uint32 = 0x2D1400

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

// ReadDriveHealth reads the SMART / Health log of an NVMe drive. Other bus
// types return ErrHealthUnsupported: reading ATA SMART data on Windows
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

	desc, err := deviceDescriptor(disk)
	if err != nil {
		return DriveHealth{}, err
	}
	if desc.bus != busTypeNvme {
		return DriveHealth{}, fmt.Errorf("%w: %s is not NVMe", ErrHealthUnsupported, desc.model)
	}
	log, err := nvmeHealthLog(disk)
	if err != nil {
		return DriveHealth{}, err
	}
	h := parseNVMeHealthLog(log)
	h.Model, h.Serial = desc.model, desc.serial
	return h, nil
}

// descriptor is what homecloud uses of a STORAGE_DEVICE_DESCRIPTOR.
type descriptor struct {
	model, serial string
	bus           uint32
}

// deviceDescriptor reads the disk's STORAGE_DEVICE_DESCRIPTOR.
func deviceDescriptor(disk windows.Handle) (descriptor, error) {
	var query [propertyQueryHeader + 4]byte
	binary.LittleEndian.PutUint32(query[0:], storageDeviceProperty)
	binary.LittleEndian.PutUint32(query[4:], propertyStandardQuery)
	var out [deviceDescriptorBufSize]byte
	var n uint32
	if err := windows.DeviceIoControl(disk, ioctlStorageQueryProperty, &query[0], uint32(len(query)), &out[0], uint32(len(out)), &n, nil); err != nil {
		return descriptor{}, fmt.Errorf("query device descriptor: %w", err)
	}
	return parseDeviceDescriptor(out[:n])
}

// parseDeviceDescriptor reads STORAGE_DEVICE_DESCRIPTOR: ProductIdOffset at
// byte 16 and SerialNumberOffset at byte 24 (each a NUL-terminated string, 0
// when absent) and BusType at byte 28.
func parseDeviceDescriptor(b []byte) (descriptor, error) {
	const productIDOffset, serialNumberOffset, busTypeOffset = 16, 24, 28
	if len(b) < busTypeOffset+4 {
		return descriptor{}, fmt.Errorf("device descriptor too short (%d bytes)", len(b))
	}
	return descriptor{
		model:  descriptorString(b, binary.LittleEndian.Uint32(b[productIDOffset:])),
		serial: descriptorString(b, binary.LittleEndian.Uint32(b[serialNumberOffset:])),
		bus:    binary.LittleEndian.Uint32(b[busTypeOffset:]),
	}, nil
}

// descriptorString reads the NUL-terminated string at off; 0 (and an offset
// past the end, which some drivers write for "none") mean absent.
func descriptorString(b []byte, off uint32) string {
	if off == 0 || int(off) >= len(b) {
		return ""
	}
	s := b[off:]
	if end := bytes.IndexByte(s, 0); end >= 0 {
		s = s[:end]
	}
	return strings.TrimSpace(string(s))
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
