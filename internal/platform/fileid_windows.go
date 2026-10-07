package platform

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// FileID returns what identifies an open file on its system: hard links to
// one file share it (GetFileInformationByHandle: the volume serial number
// and the file index).
func FileID(f *os.File) (FileIdentity, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return FileIdentity{}, fmt.Errorf("file information of %s: %w", f.Name(), err)
	}
	return FileIdentity{
		Volume: uint64(info.VolumeSerialNumber),
		Index:  uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow),
	}, nil
}
