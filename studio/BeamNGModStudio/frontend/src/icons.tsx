import type { SVGProps } from 'react'

export type IconName =
  | 'library' | 'workspace' | 'activity' | 'scan' | 'search' | 'vehicle' | 'map'
  | 'code' | 'mixed' | 'unknown' | 'link' | 'unlink' | 'archive' | 'files'
  | 'warning' | 'check' | 'error' | 'arrow' | 'plus' | 'save' | 'trash'
  | 'edit' | 'diff' | 'agent' | 'play' | 'export' | 'install' | 'terminal'
  | 'book' | 'close' | 'copy' | 'refresh' | 'folder' | 'settings' | 'chevron'
  | 'sun' | 'moon' | 'collapse' | 'more' | 'columns' | 'shield'

const paths: Record<IconName, JSX.Element> = {
  library: <><path d="M4 5.5h16v14H4z"/><path d="M7 9h10M7 13h10M7 17h6"/></>,
  workspace: <><path d="M3.5 7.5h6l2-2h9v13h-17z"/><path d="M8 12h8M12 8v8"/></>,
  activity: <><path d="M3 12h4l2-6 4 12 2-6h6"/></>,
  scan: <><path d="M4 8V4h4M16 4h4v4M20 16v4h-4M8 20H4v-4"/><path d="M7 12h10"/></>,
  search: <><circle cx="10.5" cy="10.5" r="6.5"/><path d="m15.5 15.5 5 5"/></>,
  vehicle: <><path d="m4 14 1.7-5h12.6l1.7 5v4H4z"/><path d="m7 9 1.5-3h7L17 9"/><circle cx="7.5" cy="17" r="1.5"/><circle cx="16.5" cy="17" r="1.5"/></>,
  map: <><path d="m3.5 6 5-2 7 2 5-2v14l-5 2-7-2-5 2z"/><path d="M8.5 4v14M15.5 6v14"/></>,
  code: <><path d="m9 7-5 5 5 5M15 7l5 5-5 5M13 4l-2 16"/></>,
  mixed: <><rect x="4" y="4" width="7" height="7"/><rect x="13" y="4" width="7" height="7"/><rect x="4" y="13" width="7" height="7"/><path d="M16.5 13v7M13 16.5h7"/></>,
  unknown: <><circle cx="12" cy="12" r="9"/><path d="M9.8 9a2.5 2.5 0 1 1 3.7 2.2c-1 .6-1.5 1.1-1.5 2.3M12 17h.01"/></>,
  link: <><path d="M10 13a5 5 0 0 0 7.5.5l2-2a5 5 0 0 0-7-7l-1.2 1.2"/><path d="M14 11a5 5 0 0 0-7.5-.5l-2 2a5 5 0 0 0 7 7l1.2-1.2"/></>,
  unlink: <><path d="m3 3 18 18M10.5 6.5a5 5 0 0 1 6.8-2l2.2 2a5 5 0 0 1 .5 6.8M13.5 17.5a5 5 0 0 1-6.8 2l-2.2-2A5 5 0 0 1 4 10.7"/></>,
  archive: <><path d="M4 7h16v13H4zM3 4h18v3H3z"/><path d="M9 11h6"/></>,
  files: <><path d="M6 3h8l4 4v14H6z"/><path d="M14 3v5h5M9 12h6M9 16h6"/></>,
  warning: <><path d="M12 3 2.8 20h18.4z"/><path d="M12 9v5M12 17h.01"/></>,
  check: <path d="m4 12 5 5L20 6"/>,
  error: <><circle cx="12" cy="12" r="9"/><path d="m9 9 6 6M15 9l-6 6"/></>,
  arrow: <><path d="M5 12h14M13 6l6 6-6 6"/></>,
  plus: <path d="M12 5v14M5 12h14"/>,
  save: <><path d="M5 3h12l2 2v16H5z"/><path d="M8 3v6h8V3M8 21v-7h8v7"/></>,
  trash: <><path d="M4 7h16M9 3h6l1 4H8zM6 7l1 14h10l1-14M10 11v6M14 11v6"/></>,
  edit: <><path d="m4 20 4.5-1 10-10-3.5-3.5-10 10z"/><path d="m13.5 7 3.5 3.5"/></>,
  diff: <><path d="M7 4v16M4 7l3-3 3 3M17 20V4M14 17l3 3 3-3"/></>,
  agent: <><rect x="5" y="7" width="14" height="12" rx="2"/><path d="M9 12h.01M15 12h.01M9 16h6M12 7V3M9 3h6"/></>,
  play: <path d="m8 5 11 7-11 7z"/>,
  export: <><path d="M12 4v11M8 8l4-4 4 4"/><path d="M5 13v7h14v-7"/></>,
  install: <><path d="M12 3v12M7 10l5 5 5-5"/><path d="M5 19h14"/></>,
  terminal: <><rect x="3" y="4" width="18" height="16" rx="2"/><path d="m7 9 3 3-3 3M12 15h5"/></>,
  book: <><path d="M4 5.5A3.5 3.5 0 0 1 7.5 2H11v17H7.5A3.5 3.5 0 0 0 4 22z"/><path d="M20 5.5A3.5 3.5 0 0 0 16.5 2H13v17h3.5A3.5 3.5 0 0 1 20 22z"/></>,
  close: <path d="m6 6 12 12M18 6 6 18"/>,
  copy: <><rect x="8" y="8" width="11" height="11" rx="1"/><path d="M16 8V5H5v11h3"/></>,
  refresh: <><path d="M20 7v5h-5M4 17v-5h5"/><path d="M18.2 9A7 7 0 0 0 6.4 6.4L4 9M5.8 15A7 7 0 0 0 17.6 17.6L20 15"/></>,
  settings: <><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06A1.7 1.7 0 0 0 15 19.4a1.7 1.7 0 0 0-1 .6 1.7 1.7 0 0 0-.4 1.1V21h-4v-.09A1.7 1.7 0 0 0 8.5 19.4a1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-.6-1 1.7 1.7 0 0 0-1.1-.4H3v-4h.09A1.7 1.7 0 0 0 4.6 8.5a1.7 1.7 0 0 0-.34-1.88l-.06-.06 2.83-2.83.06.06A1.7 1.7 0 0 0 9 4.6a1.7 1.7 0 0 0 1-.6 1.7 1.7 0 0 0 .4-1.1V3h4v.09A1.7 1.7 0 0 0 15.5 4.6a1.7 1.7 0 0 0 1.88-.34l.06-.06 2.83 2.83-.06.06A1.7 1.7 0 0 0 19.4 9c.13.38.35.72.64 1 .3.28.68.42 1.1.4H21v4h-.09A1.7 1.7 0 0 0 19.4 15z"/></>,
  folder: <path d="M3 6h7l2 2h9v11H3z"/>,
  chevron: <path d="m8 10 4 4 4-4"/>,
  sun: <><circle cx="12" cy="12" r="3.5"/><path d="M12 2v2M12 20v2M4.93 4.93l1.42 1.42M17.65 17.65l1.42 1.42M2 12h2M20 12h2M4.93 19.07l1.42-1.42M17.65 6.35l1.42-1.42"/></>,
  moon: <path d="M20 15.5A8.5 8.5 0 0 1 8.5 4 8.5 8.5 0 1 0 20 15.5z"/>,
  collapse: <><path d="m9 7-5 5 5 5M20 5v14"/></>,
  more: <><circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/></>,
  columns: <><rect x="3" y="4" width="18" height="16" rx="1"/><path d="M9 4v16M15 4v16"/></>,
  shield: <><path d="M12 3 5 6v5c0 4.8 2.8 8.1 7 10 4.2-1.9 7-5.2 7-10V6z"/><path d="m9 12 2 2 4-5"/></>,
}

export function Icon({ name, size = 18, ...props }: { name: IconName; size?: number } & SVGProps<SVGSVGElement>) {
  return <svg viewBox="0 0 24 24" width={size} height={size} fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>{paths[name]}</svg>
}

export function BeamWorldsMark({ size = 28, className = '' }: { size?: number; className?: string }) {
  return <svg className={`beamworlds-mark ${className}`} viewBox="0 0 36 32" width={size} height={size} fill="none" aria-hidden="true">
    <path d="M3.5 4.5h8.2c4.1 0 6.4 2.1 6.4 5.3 0 2.2-1.1 3.8-3.2 4.7 2.7.8 4.1 2.7 4.1 5.7 0 4.5-3 7.3-7.8 7.3H3.5zM8.2 8.8v4h3.1c1.5 0 2.3-.7 2.3-2s-.8-2-2.3-2zm0 8.1v6.2h3.4c1.9 0 2.8-1 2.8-3.1 0-2-.9-3.1-2.8-3.1z" fill="currentColor"/>
    <path d="m18.2 4.5 3.5 23h4.4l2.2-11.1 2.2 11.1h3.9l1.9-23h-4.4l-.6 13.1-2.4-10.4h-1.2l-2.4 10.4-1.8-13.1z" fill="currentColor"/>
  </svg>
}
