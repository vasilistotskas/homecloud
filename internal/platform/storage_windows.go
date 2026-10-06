package platform

import (
	"encoding/binary"
	"fmt"
	"slices"
	"strings"

	"golang.org/x/sys/windows"
)

const (
	// ioctlVolumeGetVolumeDiskExtents is IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS:
	// CTL_CODE(IOCTL_VOLUME_BASE 'V', 0, METHOD_BUFFERED, FILE_ANY_ACCESS).
	ioctlVolumeGetVolumeDiskExtents uint32 = 0x00560000
	// VOLUME_DISK_EXTENTS: NumberOfDiskExtents (DWORD), then DISK_EXTENT
	// entries at byte 8, each 24 bytes with DiskNumber (DWORD) first.
	diskExtentsHeader = 8
	diskExtentSize    = 24
	// maxDiskExtents is room for a volume spread over many disks; a reply
	// that does not fit fails with ERROR_MORE_DATA.
	maxDiskExtents = 64
)

// StorageOf finds the physical disks holding path's volume (more than one
// for a spanned, striped or mirrored dynamic volume). Windows reports no md
// arrays; a Storage Spaces volume names its virtual disk, whose health
// ReadDriveHealth cannot read.
func StorageOf(path string) (Storage, error) {
	vol, err := volumeName(path)
	if err != nil {
		return Storage{}, err
	}
	// CreateFile opens a volume by its GUID name without the trailing backslash.
	name, err := windows.UTF16PtrFromString(strings.TrimSuffix(vol, `\`))
	if err != nil {
		return Storage{}, fmt.Errorf("encode volume name: %w", err)
	}
	h, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return Storage{}, fmt.Errorf("open volume %s: %w", vol, err)
	}
	defer windows.CloseHandle(h) //nolint:errcheck // read-only query handle

	var out [diskExtentsHeader + maxDiskExtents*diskExtentSize]byte
	var n uint32
	if ioErr := windows.DeviceIoControl(h, ioctlVolumeGetVolumeDiskExtents, nil, 0, &out[0], uint32(len(out)), &n, nil); ioErr != nil {
		return Storage{}, fmt.Errorf("disks of volume %s: %w", vol, ioErr)
	}
	disks, err := parseDiskExtents(out[:n])
	if err != nil {
		return Storage{}, fmt.Errorf("disks of volume %s: %w", vol, err)
	}
	var s Storage
	for _, d := range disks {
		s.Drives = append(s.Drives, Drive{
			Name:   fmt.Sprintf("PhysicalDrive%d", d),
			Device: fmt.Sprintf(`\\.\PhysicalDrive%d`, d),
		})
	}
	return s, nil
}

// parseDiskExtents returns the distinct disk numbers of a VOLUME_DISK_EXTENTS
// reply, in ascending order.
func parseDiskExtents(b []byte) ([]uint32, error) {
	if len(b) < diskExtentsHeader {
		return nil, fmt.Errorf("disk extents reply too short (%d bytes)", len(b))
	}
	count := int(binary.LittleEndian.Uint32(b))
	if count == 0 || count > (len(b)-diskExtentsHeader)/diskExtentSize {
		return nil, fmt.Errorf("disk extents reply has %d extents in %d bytes", count, len(b))
	}
	var disks []uint32
	for i := range count {
		d := binary.LittleEndian.Uint32(b[diskExtentsHeader+i*diskExtentSize:])
		if !slices.Contains(disks, d) {
			disks = append(disks, d)
		}
	}
	slices.Sort(disks)
	return disks, nil
}
