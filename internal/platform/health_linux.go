package platform

import (
	"context"
	"fmt"
	"math"
	"slices"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

// udisks2 D-Bus names (https://storaged.org/doc/udisks2-api/latest/). Reading
// SMART properties needs no authorization; udisks refreshes them every 10 minutes.
const (
	udisksBus        = "org.freedesktop.UDisks2"
	udisksManager    = "/org/freedesktop/UDisks2/Manager"
	ifaceManager     = "org.freedesktop.UDisks2.Manager"
	ifaceBlock       = "org.freedesktop.UDisks2.Block"
	ifaceDrive       = "org.freedesktop.UDisks2.Drive"
	ifaceDriveAta    = "org.freedesktop.UDisks2.Drive.Ata"
	ifaceNVMe        = "org.freedesktop.UDisks2.NVMe.Controller"
	ifaceProperties  = "org.freedesktop.DBus.Properties"
	secondsPerHour   = 3600
	kelvinOffsetATA  = 273.15
	noDriveObjectRef = dbus.ObjectPath("/")
)

// DriveHealthOf reads the SMART data udisks2 keeps for the drive holding path
// (NVMe controllers and ATA drives). Drives udisks cannot map, such as
// device-mapper volumes, return ErrHealthUnsupported.
func DriveHealthOf(ctx context.Context, path string) (DriveHealth, error) {
	// The filesystem UUID identifies the volume however it is mounted (for
	// example as /dev/root), and udisks resolves it through Block.IdUUID.
	uuid, err := VolumeID(path)
	if err != nil {
		return DriveHealth{}, err
	}
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return DriveHealth{}, fmt.Errorf("connect to the system bus: %w", err)
	}
	defer conn.Close() //nolint:errcheck // nothing to flush

	var blocks []dbus.ObjectPath
	if callErr := conn.Object(udisksBus, udisksManager).CallWithContext(ctx, ifaceManager+".ResolveDevice", 0,
		map[string]dbus.Variant{"uuid": dbus.MakeVariant(uuid)}, map[string]dbus.Variant{}).Store(&blocks); callErr != nil {
		return DriveHealth{}, fmt.Errorf("udisks: resolve filesystem %s: %w", uuid, callErr)
	}
	if len(blocks) == 0 {
		return DriveHealth{}, fmt.Errorf("%w: udisks does not know filesystem %s", ErrHealthUnsupported, uuid)
	}
	var drivePath dbus.ObjectPath
	if propErr := conn.Object(udisksBus, blocks[0]).StoreProperty(ifaceBlock+".Drive", &drivePath); propErr != nil {
		return DriveHealth{}, fmt.Errorf("udisks: drive of filesystem %s: %w", uuid, propErr)
	}
	if drivePath == noDriveObjectRef {
		return DriveHealth{}, fmt.Errorf("%w: filesystem %s is not on a physical drive udisks can see", ErrHealthUnsupported, uuid)
	}
	drive := conn.Object(udisksBus, drivePath)
	var model string
	if propErr := drive.StoreProperty(ifaceDrive+".Model", &model); propErr != nil {
		return DriveHealth{}, fmt.Errorf("udisks: model of %s: %w", drivePath, propErr)
	}

	node, err := introspect.Call(drive)
	if err != nil {
		return DriveHealth{}, fmt.Errorf("udisks: introspect %s: %w", drivePath, err)
	}
	implements := func(iface string) bool {
		return slices.ContainsFunc(node.Interfaces, func(i introspect.Interface) bool { return i.Name == iface })
	}

	switch {
	case implements(ifaceNVMe):
		nvme, err := allProperties(ctx, drive, ifaceNVMe)
		if err != nil {
			return DriveHealth{}, err
		}
		var attrs map[string]dbus.Variant
		if err := drive.CallWithContext(ctx, ifaceNVMe+".SmartGetAttributes", 0, map[string]dbus.Variant{}).Store(&attrs); err != nil {
			return DriveHealth{}, fmt.Errorf("udisks: NVMe SMART attributes of %s: %w", drivePath, err)
		}
		return parseUdisksNVMe(model, nvme, attrs)
	case implements(ifaceDriveAta):
		ata, err := allProperties(ctx, drive, ifaceDriveAta)
		if err != nil {
			return DriveHealth{}, err
		}
		return parseUdisksATA(model, ata)
	}
	return DriveHealth{}, fmt.Errorf("%w: %s (%s) reports no SMART data", ErrHealthUnsupported, drivePath, model)
}

// allProperties returns the properties of iface on obj.
func allProperties(ctx context.Context, obj dbus.BusObject, iface string) (map[string]dbus.Variant, error) {
	var props map[string]dbus.Variant
	if err := obj.CallWithContext(ctx, ifaceProperties+".GetAll", 0, iface).Store(&props); err != nil {
		return nil, fmt.Errorf("udisks: %s properties of %s: %w", iface, obj.Path(), err)
	}
	return props, nil
}

func variant[T any](m map[string]dbus.Variant, key string) (T, error) {
	var v T
	raw, ok := m[key]
	if !ok {
		return v, fmt.Errorf("udisks: missing %s", key)
	}
	if err := raw.Store(&v); err != nil {
		return v, fmt.Errorf("udisks: %s: %w", key, err)
	}
	return v, nil
}

// parseUdisksNVMe maps the NVMe.Controller properties and SmartGetAttributes
// result (udisks ≥ 2.10) to DriveHealth.
func parseUdisksNVMe(model string, props, attrs map[string]dbus.Variant) (DriveHealth, error) {
	updated, err := variant[uint64](props, "SmartUpdated")
	if err != nil {
		return DriveHealth{}, err
	}
	if updated == 0 {
		return DriveHealth{}, fmt.Errorf("%w: udisks has not read this drive's SMART data yet", ErrHealthUnsupported)
	}
	warnings, err := variant[[]string](props, "SmartCriticalWarning")
	if err != nil {
		return DriveHealth{}, err
	}
	hours, err := variant[uint64](props, "SmartPowerOnHours")
	if err != nil {
		return DriveHealth{}, err
	}
	kelvin, err := variant[uint16](props, "SmartTemperature")
	if err != nil {
		return DriveHealth{}, err
	}
	spare, err := variant[byte](attrs, "avail_spare")
	if err != nil {
		return DriveHealth{}, err
	}
	used, err := variant[byte](attrs, "percent_used")
	if err != nil {
		return DriveHealth{}, err
	}
	media, err := variant[uint64](attrs, "media_errors")
	if err != nil {
		return DriveHealth{}, err
	}
	h := DriveHealth{
		Model:                 model,
		Protocol:              "NVMe",
		Warnings:              warnings,
		AvailableSparePercent: new(int(spare)),
		PercentUsed:           new(int(used)),
		MediaErrors:           new(media),
		PowerOnHours:          new(hours),
	}
	if kelvin != 0 {
		h.TemperatureCelsius = new(kelvinToCelsius(int(kelvin)))
	}
	return h, nil
}

// parseUdisksATA maps the Drive.Ata SMART properties to DriveHealth.
func parseUdisksATA(model string, props map[string]dbus.Variant) (DriveHealth, error) {
	for key, missing := range map[string]string{"SmartSupported": "supported", "SmartEnabled": "enabled"} {
		on, err := variant[bool](props, key)
		if err != nil {
			return DriveHealth{}, err
		}
		if !on {
			return DriveHealth{}, fmt.Errorf("%w: SMART is not %s on this drive", ErrHealthUnsupported, missing)
		}
	}
	updated, err := variant[uint64](props, "SmartUpdated")
	if err != nil {
		return DriveHealth{}, err
	}
	if updated == 0 {
		return DriveHealth{}, fmt.Errorf("%w: udisks has not read this drive's SMART data yet", ErrHealthUnsupported)
	}
	failing, err := variant[bool](props, "SmartFailing")
	if err != nil {
		return DriveHealth{}, err
	}
	kelvin, err := variant[float64](props, "SmartTemperature")
	if err != nil {
		return DriveHealth{}, err
	}
	seconds, err := variant[uint64](props, "SmartPowerOnSeconds")
	if err != nil {
		return DriveHealth{}, err
	}
	bad, err := variant[int64](props, "SmartNumBadSectors")
	if err != nil {
		return DriveHealth{}, err
	}
	h := DriveHealth{Model: model, Protocol: "ATA", PowerOnHours: new(seconds / secondsPerHour)}
	if failing {
		h.Warnings = []string{"failing"}
	}
	if kelvin > 0 {
		h.TemperatureCelsius = new(int(math.Round(kelvin - kelvinOffsetATA)))
	}
	if bad >= 0 { // udisks reports -1 when unknown
		h.MediaErrors = new(uint64(bad))
	}
	return h, nil
}
