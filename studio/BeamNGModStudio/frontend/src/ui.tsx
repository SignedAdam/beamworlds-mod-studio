import type { ButtonHTMLAttributes, ReactNode } from 'react'
import { Icon, type IconName } from './icons'

export function Button({ icon, tone = 'default', children, className = '', ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { icon?: IconName; tone?: 'default' | 'primary' | 'danger' | 'quiet'; children: ReactNode }) {
  return <button className={`button button--${tone} ${className}`} {...props}>{icon && <Icon name={icon} size={16}/>}<span>{children}</span></button>
}

export function Badge({ children, tone = 'neutral' }: { children: ReactNode; tone?: 'neutral' | 'success' | 'warning' | 'danger' | 'accent' | 'cyan' }) {
  return <span className={`badge badge--${tone}`}>{children}</span>
}

export function EmptyState({ icon, title, detail, action }: { icon: IconName; title: string; detail?: string; action?: ReactNode }) {
  return <div className="empty-state"><div className="empty-state__icon"><Icon name={icon} size={28}/></div><h3>{title}</h3>{detail && <p>{detail}</p>}{action}</div>
}

export function Spinner({ small = false }: { small?: boolean }) {
  return <span className={small ? 'spinner spinner--small' : 'spinner'} aria-label="Loading"/>
}
export function HelpTip({ label, children }: { label: string; children: ReactNode }) {
  return <span className="help-tip"><button type="button" aria-label={label}>?</button><span role="tooltip">{children}</span></span>
}


export function formatBytes(value = 0): string {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const exponent = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1)
  const amount = value / 1024 ** exponent
  return `${amount >= 100 || exponent === 0 ? amount.toFixed(0) : amount.toFixed(1)} ${units[exponent]}`
}

export function formatDate(value?: string | Date): string {
  if (!value) return 'Not recorded'
  const date = value instanceof Date ? value : new Date(value)
  if (Number.isNaN(date.valueOf())) return String(value)
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

export function shortID(value = ''): string {
  return value ? value.slice(0, 8) : '—'
}

export function kindIcon(kind = 'unknown'): IconName {
  if (kind === 'vehicle') return 'vehicle'
  if (kind === 'map') return 'map'
  if (kind === 'ui' || kind === 'script') return 'code'
  if (kind === 'mixed') return 'mixed'
  return 'unknown'
}

export function kindLabel(kind = 'unknown'): string {
  if (kind === 'ui') return 'UI app'
  return kind.charAt(0).toUpperCase() + kind.slice(1)
}

export function issueTone(severity = 'info'): 'neutral' | 'warning' | 'danger' | 'cyan' {
  if (severity === 'error') return 'danger'
  if (severity === 'warning') return 'warning'
  return 'cyan'
}
