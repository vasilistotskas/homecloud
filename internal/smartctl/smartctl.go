// Package smartctl reads drive health with smartmontools' smartctl, for Linux
// servers and the container, which have no udisks. smartctl talks to the
// drives directly, so it needs their device nodes and the capabilities to
// send them SMART commands (CAP_SYS_RAWIO for ATA, CAP_SYS_ADMIN for NVMe).
//
// Output is smartctl's JSON (--json), whose keys this package reads as
// smartmontools 7.5 writes them: smartctl.cpp (envelope and exit status),
// ataprint.cpp (ATA) and nvmeprint.cpp (NVMe).
package smartctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"regexp"
	"strings"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/platform"
	"github.com/vasilistotskas/homecloud/internal/tool"
)

// versionPattern matches the first line of `smartctl --version`, e.g.
// "smartctl 7.5 2025-04-30 r5714 [x86_64-linux-6.12.0] (local build)".
var versionPattern = regexp.MustCompile(`^smartctl (\d+\.\d+) `)

// Pin returns the version pin for the configured smartctl.
func Pin(cfg config.Smartctl) tool.Pin {
	return tool.Pin{
		Name:    "smartctl",
		Binary:  cfg.Binary,
		Args:    []string{"--version"},
		Pattern: versionPattern,
		Want:    cfg.Version,
	}
}

// CheckVersion fails unless the configured smartctl runs here and reports
// the pinned version.
func CheckVersion(ctx context.Context, cfg config.Smartctl) error {
	err := Pin(cfg).Check(ctx)
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%w: %w", ErrNotPermitted, err)
	}
	return err
}

// ErrNotPermitted means the system refused to start smartctl: it carries file
// capabilities, which a container must allow (cap_add SYS_RAWIO and
// SYS_ADMIN) and not suppress (no-new-privileges).
var ErrNotPermitted = errors.New("smartctl may not run with its capabilities here: allow SYS_RAWIO and SYS_ADMIN (compose cap_add) and do not set no-new-privileges")

// Read runs smartctl on a drive and returns what the drive reports.
func Read(ctx context.Context, cfg config.Smartctl, d platform.Drive) (platform.DriveHealth, error) {
	if d.Device == "" {
		return platform.DriveHealth{}, fmt.Errorf("%w: no device node here (pass the drive to the container with compose devices:)",
			platform.ErrHealthUnsupported)
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout.Std())
	defer cancel()
	out, err := platform.Command(ctx, cfg.Binary, "--json=c", "--info", "--health", "--attributes", d.Device).Output()
	if err != nil {
		// smartctl's exit status is a bit mask that also carries health
		// findings (smartctl(8), RETURN VALUES): only failing to run at all
		// is an error here; parse judges the rest.
		if _, ok := errors.AsType[*exec.ExitError](err); !ok {
			if errors.Is(err, fs.ErrPermission) {
				return platform.DriveHealth{}, ErrNotPermitted
			}
			return platform.DriveHealth{}, fmt.Errorf("run smartctl: %w", err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return platform.DriveHealth{}, fmt.Errorf("smartctl did not finish within %s: %w", cfg.Timeout.Std(), ctxErr)
		}
	}
	return parse(out)
}

// Exit status bits (smartctl(8), RETURN VALUES).
const (
	exitCommandLine = 1 << 0 // the command line did not parse
	exitDeviceOpen  = 1 << 1 // the device could not be opened, identified (IDENTIFY DEVICE) or woken
	exitPrefailNow  = 1 << 4 // a pre-failure attribute is at or below its threshold
)

// ATA attribute ids counted as bad sectors, as udisks counts them: Reallocated
// Sector Count and Current Pending Sector Count.
const (
	ataReallocatedSectors = 5
	ataPendingSectors     = 197
)

// report is the part of smartctl's JSON output homecloud reads.
type report struct {
	JSONFormatVersion []int `json:"json_format_version"`
	Smartctl          struct {
		ExitStatus int `json:"exit_status"`
		Messages   []struct {
			String string `json:"string"`
		} `json:"messages"`
	} `json:"smartctl"`
	Device struct {
		Protocol string `json:"protocol"`
	} `json:"device"`
	ModelName    string `json:"model_name"`
	SerialNumber string `json:"serial_number"`
	SmartStatus  *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature *struct {
		Current *int `json:"current"`
	} `json:"temperature"`
	PowerOnTime *struct {
		Hours *uint64 `json:"hours"`
	} `json:"power_on_time"`
	// EnduranceUsed and SpareAvailable are smartctl 7.5's device-independent
	// values (ATA device statistics or attributes, NVMe health log).
	EnduranceUsed *struct {
		CurrentPercent *int `json:"current_percent"`
	} `json:"endurance_used"`
	SpareAvailable *struct {
		CurrentPercent *int `json:"current_percent"`
	} `json:"spare_available"`
	NVMeLog *struct {
		CriticalWarning *uint8  `json:"critical_warning"`
		MediaErrors     *uint64 `json:"media_errors"`
	} `json:"nvme_smart_health_information_log"`
	ATAAttributes *struct {
		Table []struct {
			ID  int `json:"id"`
			Raw struct {
				Value uint64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
}

func (r report) messages() string {
	msgs := make([]string, 0, len(r.Smartctl.Messages))
	for _, m := range r.Smartctl.Messages {
		msgs = append(msgs, m.String)
	}
	if len(msgs) == 0 {
		return fmt.Sprintf("exit status %d", r.Smartctl.ExitStatus)
	}
	return strings.Join(msgs, "; ")
}

// parse turns smartctl's JSON output into DriveHealth.
func parse(out []byte) (platform.DriveHealth, error) {
	var r report
	if err := json.Unmarshal(out, &r); err != nil {
		return platform.DriveHealth{}, fmt.Errorf("decode smartctl output: %w", err)
	}
	if len(r.JSONFormatVersion) == 0 || r.JSONFormatVersion[0] != 1 {
		return platform.DriveHealth{}, fmt.Errorf("smartctl JSON format %v is not 1.x", r.JSONFormatVersion)
	}
	// A drive smartctl identified names its protocol; smartctl then also
	// sets the "could not open or identify" bit for drives it has no ATA
	// IDENTIFY data for, such as SCSI disks.
	if r.Device.Protocol != "" && r.Device.Protocol != "ATA" && r.Device.Protocol != "NVMe" {
		return platform.DriveHealth{}, fmt.Errorf("%w: %s drives are not read (%s)", platform.ErrHealthUnsupported, r.Device.Protocol, r.ModelName)
	}
	if r.Smartctl.ExitStatus&(exitCommandLine|exitDeviceOpen) != 0 || r.Device.Protocol == "" {
		return platform.DriveHealth{}, fmt.Errorf("smartctl could not read the drive: %s", r.messages())
	}
	if r.SmartStatus == nil {
		return platform.DriveHealth{}, fmt.Errorf("%w: smartctl reported no SMART status for %s: %s",
			platform.ErrHealthUnsupported, r.ModelName, r.messages())
	}

	h := platform.DriveHealth{Model: r.ModelName, Serial: r.SerialNumber, Protocol: r.Device.Protocol}
	switch r.Device.Protocol {
	case "NVMe":
		if r.NVMeLog == nil || r.NVMeLog.CriticalWarning == nil || r.NVMeLog.MediaErrors == nil {
			return platform.DriveHealth{}, fmt.Errorf("smartctl reported no NVMe health log for %s", r.ModelName)
		}
		h.Warnings = platform.NVMeWarnings(*r.NVMeLog.CriticalWarning)
		h.MediaErrors = r.NVMeLog.MediaErrors
	case "ATA":
		if !r.SmartStatus.Passed || r.Smartctl.ExitStatus&exitPrefailNow != 0 {
			h.Warnings = []string{"failing"}
		}
		h.MediaErrors = badSectors(r)
	}
	if r.Temperature != nil {
		h.TemperatureCelsius = r.Temperature.Current
	}
	if r.PowerOnTime != nil {
		h.PowerOnHours = r.PowerOnTime.Hours
	}
	if r.EnduranceUsed != nil {
		h.PercentUsed = r.EnduranceUsed.CurrentPercent
	}
	if r.SpareAvailable != nil {
		h.AvailableSparePercent = r.SpareAvailable.CurrentPercent
	}
	return h, nil
}

// badSectors sums the raw reallocated and pending sector counts; nil when
// the drive reports neither attribute.
func badSectors(r report) *uint64 {
	if r.ATAAttributes == nil {
		return nil
	}
	var n uint64
	found := false
	for _, a := range r.ATAAttributes.Table {
		if a.ID == ataReallocatedSectors || a.ID == ataPendingSectors {
			n += a.Raw.Value
			found = true
		}
	}
	if !found {
		return nil
	}
	return &n
}
