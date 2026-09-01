import type { AppConfig, Dashboard } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { Icon } from './icons'
import { Badge, Button, EmptyState, formatBytes, formatDate } from './ui'

export function ActivityView({ config, dashboard, onScan }: { config: AppConfig | null; dashboard: Dashboard | null; onScan: () => void }) {
  const scanRoots = config?.scanRoots ?? []
  const latestEvents = dashboard?.latestEvents ?? []
  return <section className="view activity-view">
    <header className="view-header view-header--compact"><h1>Activity</h1><Button icon="scan" onClick={onScan}>Refresh archive state</Button></header>

    <div className="activity-metrics">
      <Metric label="Entities" value={(dashboard?.entities ?? 0).toLocaleString()} icon="library"/>
      <Metric label="Artifacts" value={(dashboard?.artifacts ?? 0).toLocaleString()} icon="archive"/>
      <Metric label="Preview cache" value={formatBytes(dashboard?.cachedAssetBytes ?? 0)} icon="files"/>
      <Metric label="SQLite" value={formatBytes(dashboard?.databaseBytes ?? 0)} icon="activity"/>
      <Metric label="Last scan" value={dashboard?.lastScanStatus || 'Not scanned'} icon="scan"/>
      <Metric label="Processed" value={`${dashboard?.lastScanAnalyzed ?? 0}/${dashboard?.lastScanFound ?? 0}`} icon="check"/>
    </div>

    <div className="activity-columns">
      <section className="technical-pane">
        <header className="pane-toolbar"><strong>Application paths</strong><span/><Badge tone="success">Archives stay in place</Badge></header>
        <div className="path-list"><PathRow label="Library" value={config?.libraryDir ?? ''}/><PathRow label="Active mods" value={config?.activeModsDir ?? ''}/><PathRow label="Database" value={config?.databasePath ?? ''}/><PathRow label="Preview cache" value={config?.imageCacheDir ?? ''}/><PathRow label="Projects" value={config?.workspaceDir ?? ''}/><PathRow label="Exports" value={config?.exportDir ?? ''}/><PathRow label="BeamNG" value={config?.gameExecutable ?? 'Executable not found'}/></div>
        <div className="scan-root-list"><strong>Scan roots</strong>{scanRoots.map(root => <code key={root}>{root}</code>)}</div>
      </section>

      <section className="technical-pane activity-log">
        <header className="pane-toolbar"><strong>Recent transitions</strong><span/>{dashboard?.lastScanAt && <time>Scan {formatDate(dashboard.lastScanAt)}</time>}</header>
        {latestEvents.length === 0 ? <EmptyState icon="activity" title="No recorded transitions" detail="Scans, archive links, projects, exports, tests, and launches appear here."/> : <div className="activity-table">{latestEvents.map(event => <div key={event.id}><Icon name={eventIcon(event.type)} size={14}/><strong>{event.type.replace(/_/g, ' ')}</strong><span>{Object.values(event.data ?? {}).filter(value => typeof value === 'string').join(' · ') || `Entity ${event.entityId.slice(0, 8)}`}</span><time>{formatDate(event.at)}</time></div>)}</div>}
      </section>
    </div>
  </section>
}

function Metric({ label, value, icon }: { label: string; value: string; icon: Parameters<typeof Icon>[0]['name'] }) {
  return <div><Icon name={icon} size={15}/><span>{label}</span><strong>{value}</strong></div>
}

function PathRow({ label, value }: { label: string; value: string }) {
  return <div><span>{label}</span><code title={value}>{value || 'Not configured'}</code><button className="icon-button" onClick={() => { if (value) void navigator.clipboard.writeText(value) }} title="Copy path"><Icon name="copy" size={13}/></button></div>
}

function eventIcon(eventType: string): Parameters<typeof Icon>[0]['name'] {
  if (eventType.includes('workspace')) return 'workspace'
  if (eventType.includes('export')) return 'export'
  if (eventType.includes('test')) return 'install'
  if (eventType.includes('launch')) return 'play'
  if (eventType.includes('unlink')) return 'unlink'
  if (eventType.includes('archive')) return 'archive'
  return 'activity'
}
