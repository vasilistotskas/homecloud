import {
  AssetState,
  DriveFileChange,
  HealthState,
  IPhoneBackupCopyChange,
  DriveSyncOutcome,
  IPhoneBackupState,
  MediaType,
  QuarantineRepairStep,
  Tier,
} from "../gen/homecloud/agent/v3/agent_pb";

/** The badge colours the app uses. */
export type BadgeColor = "brand" | "success" | "warning" | "danger" | "informative";

/** A short label and its badge colour. */
export interface Labelled {
  label: string;
  color: BadgeColor;
}

export const tierLabel: Record<Tier, string> = {
  [Tier.UNSPECIFIED]: "Unknown",
  [Tier.CONTENT]: "Checksum",
  [Tier.STRUCTURE]: "Structure",
  [Tier.PROBE]: "Playability",
};

export const iphoneStateBadge: Record<IPhoneBackupState, Labelled> = {
  [IPhoneBackupState.UNSPECIFIED]: { label: "Unknown", color: "informative" },
  [IPhoneBackupState.OK]: { label: "Healthy", color: "success" },
  [IPhoneBackupState.IN_PROGRESS]: { label: "Backing up", color: "informative" },
  [IPhoneBackupState.SHRUNK]: { label: "Much smaller", color: "warning" },
  [IPhoneBackupState.STALE]: { label: "Out of date", color: "warning" },
  [IPhoneBackupState.INCOMPLETE]: { label: "Didn't finish", color: "warning" },
  [IPhoneBackupState.UNENCRYPTED]: { label: "Not encrypted", color: "danger" },
  [IPhoneBackupState.UNREADABLE]: { label: "Unreadable files", color: "danger" },
  [IPhoneBackupState.INVALID]: { label: "Not a backup", color: "danger" },
};

/** How serious an iPhone backup state is: higher is worse. */
export const iphoneSeverity: Record<IPhoneBackupState, number> = {
  [IPhoneBackupState.UNSPECIFIED]: 0,
  [IPhoneBackupState.OK]: 0,
  [IPhoneBackupState.IN_PROGRESS]: 1,
  [IPhoneBackupState.SHRUNK]: 2,
  [IPhoneBackupState.STALE]: 3,
  [IPhoneBackupState.INCOMPLETE]: 4,
  [IPhoneBackupState.UNENCRYPTED]: 5,
  [IPhoneBackupState.UNREADABLE]: 6,
  [IPhoneBackupState.INVALID]: 7,
};

export const healthBadge: Record<HealthState, Labelled> = {
  [HealthState.UNSPECIFIED]: { label: "Not checked yet", color: "informative" },
  [HealthState.OK]: { label: "Healthy", color: "success" },
  [HealthState.WARNING]: { label: "Warning", color: "warning" },
  [HealthState.FAILING]: { label: "Failing", color: "danger" },
  [HealthState.UNAVAILABLE]: { label: "Health unavailable", color: "informative" },
};

export const iphoneCopyChangeBadge: Record<IPhoneBackupCopyChange, Labelled> = {
  [IPhoneBackupCopyChange.UNSPECIFIED]: { label: "iPhone copy", color: "informative" },
  [IPhoneBackupCopyChange.COPIED]: { label: "Backup copied", color: "success" },
  [IPhoneBackupCopyChange.REMOVED]: { label: "Copy removed", color: "informative" },
  [IPhoneBackupCopyChange.DAMAGED]: { label: "Copy damaged", color: "danger" },
  [IPhoneBackupCopyChange.PUSH_REFUSED]: { label: "Push refused", color: "warning" },
};

/** What a running md sync action is doing, for people. */
export const raidSyncLabel: Record<string, string> = {
  resync: "Resyncing",
  recover: "Rebuilding",
  reshape: "Reshaping",
  check: "Checking",
  repair: "Repairing",
  frozen: "Paused",
};

export const driveSyncOutcomeBadge: Record<DriveSyncOutcome, Labelled> = {
  [DriveSyncOutcome.UNSPECIFIED]: { label: "Not synced yet", color: "informative" },
  [DriveSyncOutcome.OK]: { label: "Up to date", color: "success" },
  [DriveSyncOutcome.PARTIAL]: { label: "Some items failed", color: "warning" },
  [DriveSyncOutcome.SIGN_IN_REQUIRED]: { label: "Sign-in needed", color: "warning" },
  [DriveSyncOutcome.SKIPPED_LOW_SPACE]: { label: "Skipped: drive almost full", color: "warning" },
  [DriveSyncOutcome.ARCHIVE_UNAVAILABLE]: { label: "Skipped: archive drive missing", color: "danger" },
  [DriveSyncOutcome.ERROR]: { label: "Couldn't sync", color: "danger" },
};

/** Whether a Drive sync got as far as listing iCloud Drive, so its counts mean something. */
export function driveSyncListed(r: { listed: bigint; archived: bigint; changed: bigint; moved: bigint }): boolean {
  return r.listed > 0n || r.archived > 0n || r.changed > 0n || r.moved > 0n;
}

export const driveFileChangeBadge: Record<DriveFileChange, Labelled> = {
  [DriveFileChange.UNSPECIFIED]: { label: "iCloud Drive", color: "informative" },
  [DriveFileChange.ARCHIVED]: { label: "Archived", color: "success" },
  [DriveFileChange.CHANGED]: { label: "Changed in iCloud", color: "informative" },
  [DriveFileChange.MOVED]: { label: "Moved", color: "informative" },
  [DriveFileChange.DELETED]: { label: "Deleted in iCloud", color: "informative" },
  [DriveFileChange.RESTORED]: { label: "Back in iCloud", color: "success" },
  [DriveFileChange.DAMAGED]: { label: "Damaged copy", color: "danger" },
  [DriveFileChange.REPAIRED]: { label: "Repaired", color: "success" },
};

export const repairStepBadge: Record<QuarantineRepairStep, Labelled> = {
  [QuarantineRepairStep.UNSPECIFIED]: { label: "Repair", color: "informative" },
  [QuarantineRepairStep.REQUESTED]: { label: "Repair requested", color: "informative" },
  [QuarantineRepairStep.SET_ASIDE]: { label: "Set aside", color: "informative" },
  [QuarantineRepairStep.REPAIRED]: { label: "Repaired", color: "success" },
  [QuarantineRepairStep.ABANDONED]: { label: "Repair not possible", color: "warning" },
};

export const stateLabel: Record<AssetState, string> = {
  [AssetState.UNSPECIFIED]: "unknown",
  [AssetState.DISCOVERED]: "discovered",
  [AssetState.DOWNLOADED]: "downloaded",
  [AssetState.VERIFIED]: "verified",
  [AssetState.QUARANTINED]: "quarantined",
};

export const mediaLabel: Record<MediaType, string> = {
  [MediaType.UNSPECIFIED]: "Unknown",
  [MediaType.PHOTO]: "Photo",
  [MediaType.VIDEO]: "Video",
  [MediaType.LIVE_PHOTO_IMAGE]: "Live Photo (image)",
  [MediaType.LIVE_PHOTO_VIDEO]: "Live Photo (video)",
};
