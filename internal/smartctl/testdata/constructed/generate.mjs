// Writes the constructed smartctl 7.5 JSON outputs next to this file:
//   node generate.mjs
// Envelope as captured from smartctl 7.5-2~bpo13+1; drive sections with the
// keys smartmontools 7.5 writes (nvmeprint.cpp print_critical_warning and
// print_smart_log; ataprint.cpp print_smart_attribs, ataPrintMain).
import fs from "node:fs";
import { fileURLToPath } from "node:url";

const out = fileURLToPath(new URL(".", import.meta.url));

const envelope = (argvDevice, exitStatus) => ({
  json_format_version: [1, 0],
  smartctl: {
    version: [7, 5],
    pre_release: false,
    svn_revision: "5714",
    platform_info: "x86_64-linux-6.12.48+deb13-amd64",
    build_info: "(local build)",
    argv: ["smartctl", "--json=c", "--info", "--health", "--attributes", argvDevice],
    exit_status: exitStatus,
  },
  local_time: { time_t: 1791323866, asctime: "Tue Oct  6 21:57:46 2026 UTC" },
});

const nvme = ({ warning, media, exitStatus }) => ({
  ...envelope("/dev/nvme0n1", exitStatus),
  device: { name: "/dev/nvme0n1", info_name: "/dev/nvme0n1", type: "nvme", protocol: "NVMe" },
  model_name: "Samsung SSD 990 PRO 2TB",
  serial_number: "S7DNNJ0X123456A",
  firmware_version: "4B2QJXD7",
  nvme_pci_vendor: { id: 5197, subsystem_id: 5197 },
  smart_support: { available: true, enabled: true },
  smart_status: warning
    ? {
        passed: false,
        nvme: {
          value: warning,
          spare_below_threshold: !!(warning & 1),
          temperature_above_or_below_threshold: !!(warning & 2),
          reliability_degraded: !!(warning & 4),
          media_read_only: !!(warning & 8),
          volatile_memory_backup_failed: !!(warning & 16),
          persistent_memory_region_unreliable: !!(warning & 32),
          other: warning & ~0x3f,
        },
      }
    : { passed: true, nvme: { value: 0 } },
  nvme_smart_health_information_log: {
    nsid: -1,
    critical_warning: warning,
    temperature: 41,
    available_spare: 100,
    available_spare_threshold: 10,
    percentage_used: 3,
    data_units_read: 41234567,
    data_units_written: 38123456,
    host_reads: 512345678,
    host_writes: 498765432,
    controller_busy_time: 1234,
    power_cycles: 812,
    power_on_hours: 9587,
    unsafe_shutdowns: 41,
    media_errors: media,
    num_err_log_entries: 0,
    warning_temp_time: 0,
    critical_comp_time: 0,
    temperature_sensors: [41, 47],
  },
  temperature: { current: 41 },
  spare_available: { current_percent: 100, threshold_percent: 10 },
  endurance_used: { current_percent: 3 },
  power_cycle_count: 812,
  power_on_time: { hours: 9587 },
});

const attr = (id, name, value, worst, thresh, raw, prefailure, whenFailed = "") => ({
  id,
  name,
  value,
  worst,
  thresh,
  when_failed: whenFailed,
  flags: {
    value: prefailure ? 51 : 50,
    string: prefailure ? "POSR-K " : "-O--CK ",
    prefailure,
    updated_online: true,
    performance: false,
    error_rate: false,
    event_count: !prefailure,
    auto_keep: true,
  },
  raw: { value: raw, string: String(raw) },
});

const ataHDD = ({ passed, exitStatus, reallocated, pending, reallocatedValue = 100 }) => ({
  ...envelope("/dev/sda", exitStatus),
  device: { name: "/dev/sda", info_name: "/dev/sda [SAT]", type: "sat", protocol: "ATA" },
  model_family: "Seagate IronWolf",
  model_name: "ST4000VN006-3CW104",
  serial_number: "ZW6123AB",
  firmware_version: "SC60",
  rotation_rate: 5400,
  smart_support: { available: true, enabled: true },
  smart_status: { passed },
  ata_smart_attributes: {
    revision: 10,
    table: [
      attr(1, "Raw_Read_Error_Rate", 82, 64, 44, 172345678, true),
      attr(5, "Reallocated_Sector_Ct", reallocatedValue, reallocatedValue, 10, reallocated, true,
        reallocatedValue <= 10 ? "now" : ""),
      attr(9, "Power_On_Hours", 99, 99, 0, 1200, false),
      attr(194, "Temperature_Celsius", 34, 46, 0, 34, false),
      attr(197, "Current_Pending_Sector", 100, 100, 0, pending, false),
      attr(198, "Offline_Uncorrectable", 100, 100, 0, 0, false),
    ],
  },
  power_on_time: { hours: 1200 },
  power_cycle_count: 37,
  temperature: { current: 34 },
});

const ataSSD = {
  ...envelope("/dev/sdb", 0),
  device: { name: "/dev/sdb", info_name: "/dev/sdb [SAT]", type: "sat", protocol: "ATA" },
  model_name: "Samsung SSD 870 EVO 1TB",
  serial_number: "S6PUNX0T654321B",
  firmware_version: "SVT03B6Q",
  rotation_rate: 0,
  smart_support: { available: true, enabled: true },
  smart_status: { passed: true },
  ata_smart_attributes: {
    revision: 1,
    table: [
      attr(5, "Reallocated_Sector_Ct", 100, 100, 10, 0, true),
      attr(9, "Power_On_Hours", 97, 97, 0, 14002, false),
      attr(177, "Wear_Leveling_Count", 93, 93, 0, 71, true),
      attr(190, "Airflow_Temperature_Cel", 68, 52, 0, 32, false),
    ],
  },
  spare_available: { current_percent: 100 },
  endurance_used: { current_percent: 7 },
  power_on_time: { hours: 14002 },
  power_cycle_count: 410,
  temperature: { current: 32 },
};

const files = {
  "nvme-healthy.json": nvme({ warning: 0, media: 0, exitStatus: 0 }),
  // Critical Warning 0x05: spare below threshold + reliability degraded;
  // smartctl sets exit bit 3 ("DISK FAILING") for any critical warning.
  "nvme-critical-warning.json": nvme({ warning: 5, media: 12, exitStatus: 8 }),
  "ata-hdd-healthy.json": ataHDD({ passed: true, exitStatus: 0, reallocated: 0, pending: 0 }),
  // Remapped and pending sectors, still above the thresholds.
  "ata-hdd-bad-sectors.json": ataHDD({ passed: true, exitStatus: 0, reallocated: 56, pending: 8, reallocatedValue: 97 }),
  // SMART status FAILED with Reallocated_Sector_Ct at its threshold: exit
  // bits 3 (disk failing) and 4 (pre-failure attribute at threshold now).
  "ata-hdd-failing.json": ataHDD({ passed: false, exitStatus: 24, reallocated: 1288, pending: 16, reallocatedValue: 9 }),
  "ata-ssd-healthy.json": ataSSD,
};
for (const [name, value] of Object.entries(files)) {
  fs.writeFileSync(out + name, JSON.stringify(value) + "\n");
}
console.log(Object.keys(files).join("\n"));
