package ledger

// Event kinds recorded in the audit log.
const (
	// EventRenditionChanged: a rendition's bytes changed in iCloud.
	EventRenditionChanged = "rendition_changed"
	// EventRenditionMoved: the downloader reported a rendition at a new local
	// path; it is verified again there.
	EventRenditionMoved = "rendition_moved"
	// EventStateChanged: an asset moved between states.
	EventStateChanged = "state_changed"
	// EventQuarantined: a rendition failed verification and its asset was quarantined.
	EventQuarantined = "quarantined"
	// EventQuarantineReleased: an operator released a quarantine, with a reason.
	EventQuarantineReleased = "quarantine_released"
	// EventRepairRequested: an operator asked for the failing renditions of a
	// quarantined asset to be downloaded again.
	EventRepairRequested = "quarantine_repair_requested"
	// EventRenditionSetAside: a failing rendition's file left its recorded
	// path (kept_as names where it went; empty when it was already missing).
	EventRenditionSetAside = "rendition_set_aside"
	// EventRepaired: every rendition of a repair was downloaded again; the
	// asset left quarantine and every tier runs again.
	EventRepaired = "quarantine_repaired"
	// EventRepairAbandoned: the downloader will not download the files again;
	// the set-aside files were put back and the asset stays quarantined.
	EventRepairAbandoned = "quarantine_repair_abandoned"
	// EventDriveHealthChanged: the archive drive's health verdict changed.
	EventDriveHealthChanged = "drive_health_changed"
	// EventIPhoneBackupChanged: a device's local backup changed state (subject: its UDID).
	EventIPhoneBackupChanged = "iphone_backup_changed"
	// EventIPhoneBackupCopied: a finished backup was copied into the archive.
	EventIPhoneBackupCopied = "iphone_backup_copied"
	// EventIPhoneBackupCopyRemoved: a copy left the archive: beyond
	// iphone_backup.keep, or found missing from disk.
	EventIPhoneBackupCopyRemoved = "iphone_backup_copy_removed"
	// EventIPhoneBackupCopyDamaged: files of a copy no longer match its
	// manifest (or read again correctly: files 0).
	EventIPhoneBackupCopyDamaged = "iphone_backup_copy_damaged"
	// EventIPhoneBackupPushRefused: a backup pushed from another computer did
	// not match its manifest and was dropped; the next push sends it again.
	EventIPhoneBackupPushRefused = "iphone_backup_push_refused"
)

// Rendition versions, as the downloader names them.
const (
	// VersionOriginal is the file as captured.
	VersionOriginal = "original"
	// VersionAdjusted is the edited rendition, when the asset was edited.
	VersionAdjusted = "adjusted"
)
