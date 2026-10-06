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
	// EventDriveHealthChanged: the archive drive's health verdict changed.
	EventDriveHealthChanged = "drive_health_changed"
)

// Rendition versions, as the downloader names them.
const (
	// VersionOriginal is the file as captured.
	VersionOriginal = "original"
	// VersionAdjusted is the edited rendition, when the asset was edited.
	VersionAdjusted = "adjusted"
)
