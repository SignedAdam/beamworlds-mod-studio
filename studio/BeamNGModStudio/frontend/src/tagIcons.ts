import type { IconName } from './icons'

/**
 * Tag icons are stored as plain strings, so this list is the single source of truth for
 * which glyphs a tag may use. The Tag editor offers exactly these, and every surface that
 * renders a tag resolves stored values through it.
 */
export const TAG_ICONS = [
  'tag',
  'vehicle',
  'wheel',
  'truck',
  'plane',
  'helicopter',
  'boat',
  'map',
  'mountain',
  'globe',
  'flag',
  'code',
  'layout',
  'audio',
  'bulb',
  'box',
  'brush',
  'wrench',
  'gauge',
  'files',
  'shield',
  'star',
  'user',
] as const satisfies readonly IconName[]

export type TagIconName = (typeof TAG_ICONS)[number]

export const DEFAULT_TAG_ICON: TagIconName = 'tag'

export function isTagIcon(value: unknown): value is TagIconName {
  return typeof value === 'string' && (TAG_ICONS as readonly string[]).includes(value)
}

/** Stored icon, or the neutral tag glyph when a tag carries an unknown or empty value. */
export function safeTagIcon(value: string | undefined): TagIconName {
  const normalized = value?.trim().toLowerCase()
  return isTagIcon(normalized) ? normalized : DEFAULT_TAG_ICON
}

/** Resolves a tag colour, or `undefined` when the tag has no usable colour. */
export function tagColor(value: string | undefined): string | undefined {
  return typeof value === 'string' && /^#[0-9a-f]{6}$/i.test(value) ? value.toLowerCase() : undefined
}
