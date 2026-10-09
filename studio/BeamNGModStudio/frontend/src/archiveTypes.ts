import type { ArchiveCapability } from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";


export type DeploymentMode = "auto" | "hardlink-only" | "copy";

export const DEPLOYMENT_MODE_LABELS: Record<DeploymentMode, string> = {
  auto: "Automatic (recommended)",
  "hardlink-only": "Link only",
  copy: "Copy only",
};

export const DEPLOYMENT_MODE_DETAILS: Record<DeploymentMode, string> = {
  auto: "Links when possible, asks before copying",
  "hardlink-only": "No extra disk space, blocks mods on other drives",
  copy: "Separate files, uses extra disk space",
};

export const DEPLOYMENT_MODES: readonly DeploymentMode[] = [
  "auto",
  "hardlink-only",
  "copy",
] as const;

/** User-facing reason labels for capability reasonCodes. Unknown codes map to "". */
export function capabilityReasonLabel(reasonCode: string): string {
  switch (reasonCode) {
    case "different-volume":
      return "Different drive";
    case "filesystem-unsupported":
      return "Links unsupported";
    case "permission-denied":
      return "No access";
    case "drive-unavailable":
      return "Drive unavailable";
    case "read-only-source":
      return "Read-only folder";
    case "not-checked":
      return "Not checked";
    case "unknown":
    case "io-error":
      return "Check failed";
    default:
      return "";
  }
}

/**
 * Capability status shown under the mode options. Empty when linking works
 * everywhere, nowhere, or nothing was checked: then it changes nothing the
 * options and the disabled reason on "Link only" do not already show.
 */
export function capabilitySummary(
  capabilities: ArchiveCapability[] | null | undefined,
): { hardlinks: boolean; mixed: boolean; label: string } {
  if (!capabilities || capabilities.length === 0) {
    return { hardlinks: false, mixed: false, label: "" };
  }
  const checked = capabilities.filter((c) => c.checked);
  if (checked.length !== capabilities.length) {
    return {
      hardlinks: checked.some((capability) => capability.hardlinks),
      mixed: checked.length > 0,
      label: "Some mod folders not checked",
    };
  }
  const allLink = checked.every((c) => c.hardlinks);
  const noneLink = checked.every((c) => !c.hardlinks);
  if (allLink) return { hardlinks: true, mixed: false, label: "" };
  if (noneLink) return { hardlinks: false, mixed: false, label: "" };
  return { hardlinks: true, mixed: true, label: "Some mod folders cannot be linked" };
}
