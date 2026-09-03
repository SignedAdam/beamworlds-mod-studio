import type { ReactNode } from "react";

export type ReplaceableUIEntityCategory = "brand-mark" | "illustration" | "icon";

export interface ReplaceableUIEntity {
  readonly key: string;
  readonly category: ReplaceableUIEntityCategory;
  readonly description: string;
  readonly placeholder: "neutral" | "none";
  /** Install the finished asset here when the design is ready. */
  readonly asset?: () => ReactNode;
}

export type ReplaceableUIKey = "virgil-logo";

export const replaceableUIEntities: Record<ReplaceableUIKey, ReplaceableUIEntity> = {
  "virgil-logo": {
    key: "virgil-logo",
    category: "brand-mark",
    description: "Virgil identity mark for editor session tabs and the session header",
    placeholder: "neutral",
  },
};

export function ReplaceableUIPlaceholder({
  entity,
  className = "",
}: {
  entity: ReplaceableUIKey;
  className?: string;
}) {
  const definition = replaceableUIEntities[entity];
  const classNames = [
    "replaceable-ui-slot",
    definition.placeholder === "neutral"
      ? "replaceable-ui-slot--placeholder"
      : "",
    className,
  ]
    .filter(Boolean)
    .join(" ");
  if (definition.asset) {
    return (
      <span
        className={classNames}
        data-replaceable-ui={definition.key}
        aria-hidden="true"
      >
        {definition.asset()}
      </span>
    );
  }
  if (definition.placeholder === "none") return null;
  return (
    <span
      className={classNames}
      data-replaceable-ui={definition.key}
      aria-hidden="true"
    />
  );
}
