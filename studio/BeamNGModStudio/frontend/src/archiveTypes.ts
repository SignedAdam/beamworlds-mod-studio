import type { ArchiveCapability } from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";


export type DeploymentMode = "auto" | "hardlink-only" | "copy";

export const DEPLOYMENT_MODE_LABELS: Record<DeploymentMode, string> = {
  auto: "Automatic \u2014 recommended",
  "hardlink-only": "Hardlinks only",
  copy: "Copies \u2014 separate game files",
};

export const DEPLOYMENT_MODES: readonly DeploymentMode[] = [
  "auto",
  "hardlink-only",
  "copy",
] as const;

/** User-facing reason labels for capability reasonCodes. */
export function capabilityReasonLabel(reasonCode: string): string {
  switch (reasonCode) {
    case "different-volumes":
      return "Different volumes";
    case "filesystem-unsupported":
      return "Filesystem unsupported";
    case "permission-denied":
      return "Permission denied";
    case "drive-unavailable":
      return "Drive unavailable";
    case "not-checked":
      return "Not checked";
    case "":
      return "";
    default:
      return reasonCode;
  }
}

/** Short summary of a capability set. */
export function capabilitySummary(
  capabilities: ArchiveCapability[] | null | undefined,
): { hardlinks: boolean; mixed: boolean; label: string } {
  if (!capabilities || capabilities.length === 0) {
    return { hardlinks: false, mixed: false, label: "Not checked" };
  }
  const checked = capabilities.filter((c) => c.checked);
  if (checked.length !== capabilities.length) {
    const unavailable = capabilities.find((capability) => !capability.checked);
    return {
      hardlinks: checked.some((capability) => capability.hardlinks),
      mixed: checked.length > 0,
      label: checked.length > 0
        ? "Some folders could not be checked"
        : capabilityReasonLabel(unavailable?.reasonCode ?? "not-checked") || "Not checked",
    };
  }
  const allLink = checked.every((c) => c.hardlinks);
  const noneLink = checked.every((c) => !c.hardlinks);
  if (allLink) {
    return {
      hardlinks: true,
      mixed: false,
      label: "Hardlinks available \u00b7 no additional archive data",
    };
  }
  if (noneLink) {
    const reason = checked[0]?.reasonCode
      ? capabilityReasonLabel(checked[0].reasonCode)
      : "Copies required";
    return { hardlinks: false, mixed: false, label: reason };
  }
  return {
    hardlinks: true,
    mixed: true,
    label: "Mixed: some copies required",
  };
}
