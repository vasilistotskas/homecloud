package config

// validateDriveHealth rejects the Linux-only sections: Windows reads NVMe
// health itself.
func (c *Config) validateDriveHealth(add func(format string, args ...any)) {
	if c.DriveHealth != (DriveHealth{}) {
		add("[drive_health] applies to Linux only; Windows reads NVMe drive health itself, remove it")
	}
	if c.Smartctl != (Smartctl{}) {
		add("[smartctl] applies to Linux only; Windows reads NVMe drive health itself, remove it")
	}
}
