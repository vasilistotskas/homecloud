"""Writes the plist fixtures of internal/iphonebackup with Python's standard
plistlib, an encoder independent of homecloud's own reader.

Regenerate from the repository root (the files are committed; plist_test.go
holds the values they must decode to):

    uv run --no-project python internal/iphonebackup/testdata/plist/generate.py
"""

import datetime
import pathlib
import plistlib

HERE = pathlib.Path(__file__).parent
UTC = datetime.timezone.utc


def write(name, value, fmt):
    # aware_datetime (Python 3.13+): dates are aware and written as UTC.
    data = plistlib.dumps(value, fmt=fmt, sort_keys=True, aware_datetime=True)
    (HERE / name).write_bytes(data)


# Info.plist as Apple Devices / idevicebackup2 write it: XML, with per-app
# data and other values homecloud skips.
write(
    "info.xml",
    {
        "Device Name": "Βασίλης’s iPhone 📱",
        "Product Version": "26.1",
        "Unique Identifier": "00008110-001A2B3C4D5E801E",
        "Build Version": "23B85",
        "Last Backup Date": datetime.datetime(2026, 10, 6, 12, 0, 0, tzinfo=UTC),
        "Applications": {
            "com.example.app": {
                "PlaceholderIcon": b"\x89PNG\r\n\x1a\n" + bytes(range(256)) * 4,
                "iTunesMetadata": b"<plist/>",
            }
        },
        "Installed Applications": ["com.example.app", "com.example.other"],
        "iTunes Settings": {},
        "Target Type": "Device",
        "Serial Number": "&<>\"'",
        "Big Int": 2**63 - 1,
        "Negative": -42,
        "Ratio": 2.5,
    },
    plistlib.FMT_XML,
)

# Manifest.plist: binary, with containers and data to skip.
write(
    "manifest.bplist",
    {
        "IsEncrypted": True,
        "Version": "10.0",
        "WasPasscodeSet": False,
        "Date": datetime.datetime(2026, 10, 6, 11, 59, 58, tzinfo=UTC),
        "BackupKeyBag": bytes(range(256)) * 8,
        "Lockdown": {"DeviceName": "Βασίλης’s iPhone 📱", "ProductVersion": "26.1"},
        "Applications": {f"com.example.app{i}": {"CFBundleVersion": str(i)} for i in range(20)},
    },
    plistlib.FMT_BINARY,
)

# Status.plist: binary; the date has a fraction of a second.
write(
    "status.bplist",
    {
        "SnapshotState": "finished",
        "Date": datetime.datetime(2026, 10, 6, 12, 0, 0, 250000, tzinfo=UTC),
        "IsFullBackup": False,
        "BackupState": "new",
        "Version": "3.3",
        "UUID": "5E1F2A6C-8D3B-4C1E-9A7F-0B2C3D4E5F60",
    },
    plistlib.FMT_BINARY,
)

# Wide encodings: more than 15 entries (the count follows the marker), more
# than 255 objects (2-byte references), UTF-16 and long strings, a date
# before 2001 (a negative absolute time), 16-byte integers.
wide = {f"key{i:03d}": f"value {i}" for i in range(300)}
wide.update(
    {
        "Device Name": "Ünïcødé ✓ " * 30,
        "Long ASCII": "x" * 1000,
        "Old Date": datetime.datetime(1999, 12, 31, 23, 59, 59, tzinfo=UTC),
        "Huge": 2**64 - 1,
        "Flag": True,
    }
)
write("wide.bplist", wide, plistlib.FMT_BINARY)
