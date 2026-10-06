import { AssetState, IPhoneBackupState, MediaType, Tier } from "../gen/homecloud/agent/v1/agent_pb";

export const tierLabel: Record<Tier, string> = {
  [Tier.UNSPECIFIED]: "Unknown",
  [Tier.CONTENT]: "Checksum",
  [Tier.STRUCTURE]: "Structure",
  [Tier.PROBE]: "Playability",
};

export const iphoneStateBadge: Record<
  IPhoneBackupState,
  { label: string; color: "success" | "warning" | "danger" | "informative" }
> = {
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
