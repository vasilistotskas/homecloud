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

// ReadDriveHealth reads the SMART data udisks2 keeps for a drive (NVMe
// controllers and ATA drives), finding it by its device node. Drives udisks
// reports no SMART data for return ErrHealthUnsupported.
func ReadDriveHealth(ctx context.Context, d Drive) (DriveHealth, error) {
	if d.Device == "" {
		return DriveHealth{}, fmt.Errorf("%w: no device node here", ErrHealthUnsupported)
	}
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return DriveHealth{}, fmt.Errorf("connect to the system bus: %w", err)
	}
	defer conn.Close() //nolint:errcheck // nothing to flush

	var blocks []dbus.ObjectPath
	if callErr := conn.Object(udisksBus, udisksManager).CallWithContext(ctx, ifaceManager+".ResolveDevice", 0,
		map[string]dbus.Variant{"path": dbus.MakeVariant(d.Device)}, map[string]dbus.Variant{}).Store(&blocks); callErr != nil {
		return DriveHealth{}, fmt.Errorf("udisks: resolve %s: %w", d.Device, callErr)
	}
	if len(blocks) == 0 {
		return DriveHealth{}, fmt.Errorf("%w: udisks does not know %s", ErrHealthUnsupported, d.Device)
	}
	var drivePath dbus.ObjectPath
	if propErr := conn.Object(udisksBus, blocks[0]).StoreProperty(ifaceBlock+".Drive", &drivePath); propErr != nil {
		return DriveHealth{}, fmt.Errorf("udisks: drive of %s: %w", d.Device, propErr)
	}
	if drivePath == noDriveObjectRef {
		return DriveHealth{}, fmt.Errorf("%w: udisks sees no physical drive behind %s", ErrHealthUnsupported, d.Device)
	}
	drive := conn.Object(udisksBus, drivePath)
	var model, serial string
	if propErr := drive.StoreProperty(ifaceDrive+".Model", &model); propErr != nil {
		return DriveHealth{}, fmt.Errorf("udisks: model of %s: %w", drivePath, propErr)
	}
	if propErr := drive.StoreProperty(ifaceDrive+".Serial", &serial); propErr != nil {
		return DriveHealth{}, fmt.Errorf("udisks: serial of %s: %w", drivePath, propErr)
	}

	node, err := introspect.Call(drive)
	if err != nil {
		return DriveHealth{}, fmt.Errorf("udisks: introspect %s: %w", drivePath, err)
	}
	implements := func(iface string) bool {
		return slices.ContainsFunc(node.Interfaces, func(i introspect.Interface) bool { return i.Name == iface })
	}

	var h DriveHealth
	switch {
	case implements(ifaceNVMe):
		nvme, err := allProperties(ctx, drive, ifaceNVMe)
		if err != nil {
			return DriveHealth{}, err
		}
		var attrs map[string]dbus.Variant
		if callErr := drive.CallWithContext(ctx, ifaceNVMe+".SmartGetAttributes", 0, map[string]dbus.Variant{}).Store(&attrs); callErr != nil {
			return DriveHealth{}, fmt.Errorf("udisks: NVMe SMART attributes of %s: %w", drivePath, callErr)
		}
		if h, err = parseUdisksNVMe(nvme, attrs); err != nil {
			return DriveHealth{}, err
		}
	case implements(ifaceDriveAta):
		ata, err := allProperties(ctx, drive, ifaceDriveAta)
		if err != nil {
			return DriveHealth{}, err
		}
		if h, err = parseUdisksATA(ata); err != nil {
			return DriveHealth{}, err
		}
	default:
		return DriveHealth{}, fmt.Errorf("%w: %s (%s) reports no SMART data", ErrHealthUnsupported, drivePath, model)
	}
	h.Model, h.Serial = model, serial
	return h, nil
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
func parseUdisksNVMe(props, attrs map[string]dbus.Variant) (DriveHealth, error) {
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
func parseUdisksATA(props map[string]dbus.Variant) (DriveHealth, error) {
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
	h := DriveHealth{Protocol: "ATA", PowerOnHours: new(seconds / secondsPerHour)}
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
