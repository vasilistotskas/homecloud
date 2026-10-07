package main

// The CLI's Windows resources: its application manifest (homecloud.exe.manifest),
// compiled into one resource object per Windows architecture, which go build
// links by their file names. Regenerate after changing the manifest; CI fails
// when the objects are out of date.
//go:generate go tool goversioninfo -skip-versioninfo -manifest=homecloud.exe.manifest -64 -o=rsrc_windows_amd64.syso
//go:generate go tool goversioninfo -skip-versioninfo -manifest=homecloud.exe.manifest -64 -arm -o=rsrc_windows_arm64.syso
