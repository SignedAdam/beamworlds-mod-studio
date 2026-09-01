import { useEffect, useMemo, useState } from 'react'
import { AppService as API } from '../bindings/github.com/SignedAdam/beamng-mod-studio/index.js'
import type { LibraryItem, OrganizationState, PresetDetail, ProfileDetail, ProfileProgress } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { Badge, Button, EmptyState, Spinner, formatBytes, kindIcon } from './ui'
import { Icon } from './icons'

interface ProfilesViewProps {
  organization: OrganizationState | null
  items: LibraryItem[]
  progress: ProfileProgress | null
  onOrganization: (state: OrganizationState) => void
  onNotify: (message: string, tone?: 'success' | 'error' | 'info') => void
  onError: (error: unknown) => void
}

export function ProfilesView({ organization, items, progress, onOrganization, onNotify, onError }: ProfilesViewProps) {
  const [profileID, setProfileID] = useState('')
  const [presetID, setPresetID] = useState('')
  const [profileDetail, setProfileDetail] = useState<ProfileDetail | null>(null)
  const [presetDetail, setPresetDetail] = useState<PresetDetail | null>(null)
  const [newProfile, setNewProfile] = useState('')
  const [newPreset, setNewPreset] = useState('')
  const [newPresetDescription, setNewPresetDescription] = useState('')
  const [modQuery, setModQuery] = useState('')
  const [busy, setBusy] = useState('')
  const reusablePresets = useMemo(() => (organization?.presets ?? []).filter(preset => preset.defaultForProfileCount === 0), [organization])
  const filteredItems = useMemo(() => {
    const query = modQuery.trim().toLowerCase()
    return items.filter(item => !query || item.displayName.toLowerCase().includes(query) || item.archivePath.toLowerCase().includes(query)).slice(0, 1000)
  }, [items, modQuery])

  useEffect(() => {
    if (!profileID && organization?.profiles.length) setProfileID(organization.profiles[0].id)
    else if (profileID && !organization?.profiles.some(profile => profile.id === profileID)) setProfileID(organization?.profiles[0]?.id ?? '')
  }, [organization, profileID])

  useEffect(() => {
    if (!presetID && reusablePresets.length) setPresetID(reusablePresets[0].id)
    else if (presetID && !reusablePresets.some(preset => preset.id === presetID)) setPresetID(reusablePresets[0]?.id ?? '')
  }, [reusablePresets, presetID])

  useEffect(() => {
    if (!profileID) { setProfileDetail(null); return }
    API.GetProfile(profileID).then(setProfileDetail).catch(onError)
  }, [profileID])

  useEffect(() => {
    if (!presetID) { setPresetDetail(null); return }
    API.GetPreset(presetID).then(setPresetDetail).catch(onError)
  }, [presetID])

  const refreshOrganization = async () => onOrganization(await API.Organization())
  const refreshProfile = async () => { if (profileID) setProfileDetail(await API.GetProfile(profileID)) }
  const refreshPreset = async () => { if (presetID) setPresetDetail(await API.GetPreset(presetID)) }

  const createProfile = async () => {
    if (!newProfile.trim()) return
    setBusy('profile-create')
    try {
      const state = await API.CreateProfile(newProfile.trim())
      onOrganization(state)
      const created = state.profiles.find(profile => profile.name === newProfile.trim())
      if (created) setProfileID(created.id)
      setNewProfile('')
      onNotify('Profile created with its own default preset', 'success')
    } catch (error) { onError(error) } finally { setBusy('') }
  }

  const createPreset = async () => {
    if (!newPreset.trim()) return
    setBusy('preset-create')
    try {
      const state = await API.CreatePreset(newPreset.trim(), newPresetDescription.trim())
      onOrganization(state)
      const created = state.presets.find(preset => preset.name === newPreset.trim() && preset.defaultForProfileCount === 0)
      if (created) setPresetID(created.id)
      setNewPreset('')
      setNewPresetDescription('')
      onNotify('Reusable preset created', 'success')
    } catch (error) { onError(error) } finally { setBusy('') }
  }

  const toggleProfilePreset = async (targetPresetID: string, selected: boolean) => {
    if (!profileID) return
    try {
      await API.SetProfilePreset(profileID, targetPresetID, selected)
      await Promise.all([refreshProfile(), refreshOrganization()])
    } catch (error) { onError(error) }
  }

  const toggleDirectMod = async (entityID: string, included: boolean) => {
    if (!profileID) return
    try {
      await API.SetProfileMod(profileID, entityID, included)
      await Promise.all([refreshProfile(), refreshOrganization()])
    } catch (error) { onError(error) }
  }

  const togglePresetMod = async (entityID: string, included: boolean) => {
    if (!presetID) return
    try {
      await API.SetPresetMod(presetID, entityID, included)
      await Promise.all([refreshPreset(), refreshOrganization(), refreshProfile()])
    } catch (error) { onError(error) }
  }

  const renameProfile = async () => {
    if (!profileDetail) return
    const name = window.prompt('Profile name', profileDetail.profile.name)?.trim()
    if (!name || name === profileDetail.profile.name) return
    try { onOrganization(await API.RenameProfile(profileID, name)); await refreshProfile() } catch (error) { onError(error) }
  }

  const deleteProfile = async () => {
    if (!profileDetail || !window.confirm(`Delete profile “${profileDetail.profile.name}”? Mod archives remain untouched.`)) return
    try { onOrganization(await API.DeleteProfile(profileID)); setProfileDetail(null); onNotify('Profile deleted; source mods were not changed', 'success') } catch (error) { onError(error) }
  }

  const renamePreset = async () => {
    if (!presetDetail) return
    const name = window.prompt('Preset name', presetDetail.preset.name)?.trim()
    if (!name) return
    const description = window.prompt('Preset description', presetDetail.preset.description) ?? presetDetail.preset.description
    try { onOrganization(await API.UpdatePreset(presetID, name, description)); await Promise.all([refreshPreset(), refreshProfile()]) } catch (error) { onError(error) }
  }

  const deletePreset = async () => {
    if (!presetDetail || !window.confirm(`Delete reusable preset “${presetDetail.preset.name}”?`)) return
    try { onOrganization(await API.DeletePreset(presetID)); setPresetDetail(null); onNotify('Preset deleted', 'success') } catch (error) { onError(error) }
  }

  const activate = async (launch: boolean) => {
    if (!profileID) return
    setBusy(launch ? 'launch' : 'activate')
    try {
      if (launch) {
        const result = await API.LaunchProfile(profileID)
        onNotify(`BeamNG launched with ${result.activation.modCount} profile mods`, 'success')
      } else {
        const result = await API.ActivateProfile(profileID)
        onNotify(`Prepared ${result.modCount} mods in an isolated user folder`, 'success')
      }
    } catch (error) { onError(error) } finally { setBusy('') }
  }

  return <section className="view profiles-view" aria-label="Profiles and presets">
    <header className="view-header view-header--compact"><div><h1>Profiles</h1><p>Launch isolated mod sets without moving or disabling your existing archives.</p></div><Button icon="folder" onClick={() => API.OpenGameDirectory().catch(onError)}>Open game directory</Button></header>
    <div className="profiles-layout">
      <aside className="organization-list">
        <header><strong>Launch profiles</strong><Badge tone="neutral">{organization?.profiles.length ?? 0}</Badge></header>
        <div className="organization-create"><input value={newProfile} onChange={event => setNewProfile(event.target.value)} onKeyDown={event => { if (event.key === 'Enter') void createProfile() }} placeholder="New profile name"/><button onClick={() => void createProfile()} disabled={!newProfile.trim() || busy !== ''}><Icon name="plus" size={14}/></button></div>
        <div className="organization-list__items">{organization?.profiles.map(profile => <button key={profile.id} className={profile.id === profileID ? 'is-active' : ''} onClick={() => setProfileID(profile.id)}><Icon name="play" size={14}/><span><strong>{profile.name}</strong><small>{profile.modCount} mods · {profile.presetCount} reusable presets</small></span></button>)}</div>
        <header className="organization-list__split"><strong>Reusable presets</strong><Badge tone="neutral">{reusablePresets.length}</Badge></header>
        <div className="preset-create"><input value={newPreset} onChange={event => setNewPreset(event.target.value)} placeholder="Preset name"/><input value={newPresetDescription} onChange={event => setNewPresetDescription(event.target.value)} placeholder="Optional description"/><Button icon="plus" disabled={!newPreset.trim() || busy !== ''} onClick={createPreset}>Create preset</Button></div>
        <div className="organization-list__items">{reusablePresets.map(preset => <button key={preset.id} className={preset.id === presetID ? 'is-active' : ''} onClick={() => setPresetID(preset.id)}><Icon name="mixed" size={14}/><span><strong>{preset.name}</strong><small>{preset.modCount} mods</small></span></button>)}</div>
      </aside>

      <main className="profile-detail">
        {!profileID ? <EmptyState icon="play" title="Create a launch profile" detail="Every profile starts with a permanent default preset for mods selected directly."/> : !profileDetail ? <div className="center-loader"><Spinner/><span>Loading profile</span></div> : <>
          <header className="profile-detail__header"><div><span>LAUNCH PROFILE</span><h2>{profileDetail.profile.name}</h2><p>{profileDetail.mods.length} effective mods · isolated BeamNG user folder</p></div><div><Button tone="quiet" icon="edit" onClick={renameProfile}>Rename</Button><Button tone="quiet" icon="trash" onClick={deleteProfile}>Delete</Button><Button icon="install" disabled={busy !== ''} onClick={() => void activate(false)}>{busy === 'activate' ? 'Preparing' : 'Prepare'}</Button><Button icon="play" tone="primary" disabled={busy !== ''} onClick={() => void activate(true)}>{busy === 'launch' ? 'Launching' : 'Launch BeamNG'}</Button></div></header>
          {progress?.profileId === profileID && <div className={`profile-progress ${progress.error ? 'is-error' : ''}`}><div><strong>{progress.phase}</strong><span>{progress.current || (progress.done ? 'Complete' : 'Preparing isolated user folder')}</span><small>{progress.completed}/{progress.total} · {formatBytes(progress.bytesCopied)} copied</small></div><progress value={progress.total ? progress.completed : 0} max={Math.max(progress.total, 1)}/>{progress.error && <p>{progress.error}</p>}</div>}
          <section className="profile-section"><div className="profile-section__title"><div><h3>Preset stack</h3><p>The default preset is permanent. Reusable presets can be shared by any profile.</p></div></div><div className="profile-preset-grid">{profileDetail.presets.map(preset => <label key={preset.id} className={preset.selected ? 'is-selected' : ''}><input type="checkbox" checked={preset.selected} disabled={preset.default} onChange={event => void toggleProfilePreset(preset.id, event.target.checked)}/><span><strong>{preset.name}</strong><small>{preset.default ? 'Default · direct selections' : preset.description || 'Reusable preset'} · {preset.modCount} mods</small></span>{preset.default && <Badge tone="accent">Default</Badge>}</label>)}</div></section>
          <section className="profile-section profile-mod-section"><div className="profile-section__title"><div><h3>Direct mod selections</h3><p>Selections go into this profile’s default preset. Mods from reusable presets stay identified.</p></div><label className="search-box"><Icon name="search" size={13}/><input value={modQuery} onChange={event => setModQuery(event.target.value)} placeholder="Filter library"/></label></div><div className="membership-list">{filteredItems.map(item => {
            const effective = profileDetail.mods.find(mod => mod.entityId === item.entityId)
            const direct = Boolean(effective?.presetIds.includes(profileDetail.profile.defaultPresetId))
            const shared = effective?.presetIds.some(id => id !== profileDetail.profile.defaultPresetId)
            return <label key={item.entityId}><input type="checkbox" checked={direct} onChange={event => void toggleDirectMod(item.entityId, event.target.checked)}/><Icon name={kindIcon(String(item.kind))} size={14}/><span><strong>{item.displayName}</strong><small>{item.linked ? item.archivePath : 'Source archive missing'}</small></span>{shared && <Badge tone="cyan">Via preset</Badge>}</label>
          })}</div></section>
        </>}
      </main>

      <aside className="preset-detail">
        {!presetDetail ? <EmptyState icon="mixed" title="Reusable presets" detail="Create a preset to reuse the same mod group across launch profiles."/> : <><header><div><span>REUSABLE PRESET</span><h2>{presetDetail.preset.name}</h2><p>{presetDetail.preset.description || 'No description'}</p></div><div><button className="icon-button" onClick={renamePreset} title="Edit preset"><Icon name="edit" size={14}/></button><button className="icon-button" onClick={deletePreset} title="Delete preset"><Icon name="trash" size={14}/></button></div></header><label className="search-box search-box--wide"><Icon name="search" size={13}/><input value={modQuery} onChange={event => setModQuery(event.target.value)} placeholder="Filter library"/></label><div className="membership-list membership-list--preset">{filteredItems.map(item => <label key={item.entityId}><input type="checkbox" checked={presetDetail.entityIds.includes(item.entityId)} onChange={event => void togglePresetMod(item.entityId, event.target.checked)}/><Icon name={kindIcon(String(item.kind))} size={13}/><span><strong>{item.displayName}</strong><small>{item.linked ? formatBytes(item.sizeBytes) : 'Missing'}</small></span></label>)}</div></>}
      </aside>
    </div>
  </section>
}
