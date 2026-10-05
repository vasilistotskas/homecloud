import { AssetState, MediaType, Tier } from "../gen/homecloud/agent/v1/agent_pb";

export const tierLabel: Record<Tier, string> = {
  [Tier.UNSPECIFIED]: "Unknown",
  [Tier.CONTENT]: "Checksum",
  [Tier.STRUCTURE]: "Structure",
  [Tier.PROBE]: "Playability",
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
