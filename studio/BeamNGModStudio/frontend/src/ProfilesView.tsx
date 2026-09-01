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
  const [hasAppliedProfile, setHasAppliedProfile] = useState(false)
  const profiles = organization?.profiles ?? []
  const reusablePresets = useMemo(() => (organization?.presets ?? []).filter(preset => preset.defaultForProfileCount === 0), [organization])
  const profileMods = profileDetail?.mods ?? []
  const profilePresets = profileDetail?.presets ?? []
  const presetEntityIDs = presetDetail?.entityIds ?? []
  const filteredItems = useMemo(() => {
    const query = modQuery.trim().toLowerCase()
    return items.filter(item => !query || item.displayName.toLowerCase().includes(query) || item.archivePath.toLowerCase().includes(query)).slice(0, 1000)
  }, [items, modQuery])

  useEffect(() => {
    if (!profileID && profiles.length) setProfileID(profiles[0].id)
    else if (profileID && !profiles.some(profile => profile.id === profileID)) setProfileID(profiles[0]?.id ?? '')
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

  useEffect(() => {
    API.HasAppliedModProfile().then(setHasAppliedProfile).catch(onError)
  }, [])

  const refreshOrganization = async () => onOrganization(await API.Organization())
  const refreshProfile = async () => { if (profileID) setProfileDetail(await API.GetProfile(profileID)) }
  const refreshPreset = async () => { if (presetID) setPresetDetail(await API.GetPreset(presetID)) }

  const createProfile = async () => {
    if (!newProfile.trim()) return
    setBusy('profile-create')
    try {
      const state = await API.CreateProfile(newProfile.trim())
      onOrganization(state)
      const created = (state.profiles ?? []).find(profile => profile.name === newProfile.trim())
      if (created) setProfileID(created.id)
      setNewProfile('')
      onNotify('Mod profile created with its own default preset', 'success')
    } catch (error) { onError(error) } finally { setBusy('') }
  }

  const createPreset = async () => {
    if (!newPreset.trim()) return
    setBusy('preset-create')
    try {
      const state = await API.CreatePreset(newPreset.trim(), newPresetDescription.trim())
      onOrganization(state)
      const created = (state.presets ?? []).find(preset => preset.name === newPreset.trim() && preset.defaultForProfileCount === 0)
      if (created) setPresetID(created.id)
      await refreshProfile()
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
    try { onOrganization(await API.DeletePreset(presetID)); setPresetDetail(null); await refreshProfile(); onNotify('Preset deleted', 'success') } catch (error) { onError(error) }
  }

  const activate = async (launch: boolean) => {
    if (!profileID) return
    setBusy(launch ? 'launch' : 'activate')
    try {
      if (launch) {
        const result = await API.LaunchProfile(profileID)
        setHasAppliedProfile(true)
        onNotify(`BeamNG launched with ${result.activation.modCount} profile mods`, 'success')
      } else {
        const result = await API.ActivateProfile(profileID)
        setHasAppliedProfile(true)
        onNotify(`Applied an exact ${result.modCount}-mod selection to BeamNG`, 'success')
      }
    } catch (error) { onError(error) } finally { setBusy('') }
  }

  const restoreNormalMods = async () => {
    if (!window.confirm('Restore the enabled and disabled mods from before BeamWorlds applied a mod profile?')) return
    setBusy('restore')
    try {
      await API.RestoreNormalModSelection()
      setHasAppliedProfile(false)
      onNotify('Restored the normal BeamNG mod selection', 'success')
    } catch (error) { onError(error) } finally { setBusy('') }
  }

  return <section className="view profiles-view" aria-label="Mod profiles and presets">
    <header className="view-header view-header--compact"><div><h1>Mod profiles</h1><p>Combine reusable presets and individual mods, then launch that exact set.</p></div><div className="view-header__actions"><Button icon="refresh" disabled={!hasAppliedProfile || busy !== ''} onClick={() => void restoreNormalMods()}>Restore normal mods</Button><Button icon="folder" onClick={() => API.OpenGameDirectory().catch(onError)}>Open game directory</Button></div></header>
    <div className="profiles-layout">
      <aside className="organization-list">
        <header><strong>Mod profiles</strong><Badge tone="neutral">{profiles.length ?? 0}</Badge></header>
        <div className="organization-create"><input value={newProfile} onChange={event => setNewProfile(event.target.value)} onKeyDown={event => { if (event.key === 'Enter') void createProfile() }} placeholder="New mod profile"/><button onClick={() => void createProfile()} disabled={!newProfile.trim() || busy !== ''}><Icon name="plus" size={14}/></button></div>
        <div className="organization-list__items">{profiles.map(profile => <button key={profile.id} className={profile.id === profileID ? 'is-active' : ''} onClick={() => setProfileID(profile.id)}><Icon name="play" size={14}/><span><strong>{profile.name}</strong><small>{profile.modCount} mods · {profile.presetCount} reusable presets</small></span></button>)}</div>
        <header className="organization-list__split"><strong>Reusable presets</strong><Badge tone="neutral">{reusablePresets.length}</Badge></header>
        <div className="preset-create"><input value={newPreset} onChange={event => setNewPreset(event.target.value)} placeholder="Preset name"/><input value={newPresetDescription} onChange={event => setNewPresetDescription(event.target.value)} placeholder="Optional description"/><Button icon="plus" disabled={!newPreset.trim() || busy !== ''} onClick={createPreset}>Create preset</Button></div>
        <div className="organization-list__items">{reusablePresets.map(preset => <button key={preset.id} className={preset.id === presetID ? 'is-active' : ''} onClick={() => setPresetID(preset.id)}><Icon name="mixed" size={14}/><span><strong>{preset.name}</strong><small>{preset.modCount} mods</small></span></button>)}</div>
      </aside>

      <main className="profile-detail">
        {!profileID ? <EmptyState icon="play" title="Create a mod profile" detail="Combine reusable presets with individual mods. Each mod profile owns one permanent default preset."/> : !profileDetail ? <div className="center-loader"><Spinner/><span>Loading mod profile</span></div> : <>
          <header className="profile-detail__header"><div><span>MOD PROFILE</span><h2>{profileDetail.profile.name}</h2><p>{profileMods.length} mods · normal settings, controls, and saves stay shared</p></div><div><Button tone="quiet" icon="edit" onClick={renameProfile}>Rename</Button><Button tone="quiet" icon="trash" onClick={deleteProfile}>Delete</Button><Button icon="install" disabled={busy !== ''} onClick={() => void activate(false)}>{busy === 'activate' ? 'Applying' : 'Apply mods'}</Button><Button icon="play" tone="primary" disabled={busy !== ''} onClick={() => void activate(true)}>{busy === 'launch' ? 'Launching' : 'Launch BeamNG'}</Button></div></header>
          {progress?.profileId === profileID && <div className={`profile-progress ${progress.error ? 'is-error' : ''}`}><div><strong>{progress.phase}</strong><span>{progress.current || (progress.done ? 'Complete' : 'Applying exact mod selection')}</span><small>{progress.completed}/{progress.total} · {formatBytes(progress.bytesCopied)} copied</small></div><progress value={progress.total ? progress.completed : 0} max={Math.max(progress.total, 1)}/>{progress.error && <p>{progress.error}</p>}</div>}
          <section className="profile-section"><div className="profile-section__title"><div><h3>Included presets</h3><p>Reusable presets are combined with this mod profile’s individual mods.</p></div></div><div className="profile-preset-grid">{profilePresets.map(preset => <label key={preset.id} className={preset.selected ? 'is-selected' : ''}><input type="checkbox" checked={preset.selected} disabled={preset.default} onChange={event => void toggleProfilePreset(preset.id, event.target.checked)}/><span><strong>{preset.name}</strong><small>{preset.default ? 'Individual mods · private default preset' : preset.description || 'Reusable preset'} · {preset.modCount} mods</small></span>{preset.default && <Badge tone="accent">Default</Badge>}</label>)}</div></section>
          <section className="profile-section profile-mod-section"><div className="profile-section__title"><div><h3>Individual mods</h3><p>These selections live in this mod profile’s private default preset.</p></div><label className="search-box"><Icon name="search" size={13}/><input value={modQuery} onChange={event => setModQuery(event.target.value)} placeholder="Filter library"/></label></div><div className="membership-list">{filteredItems.map(item => {
            const effective = profileMods.find(mod => mod.entityId === item.entityId)
            const direct = Boolean((effective?.presetIds ?? []).includes(profileDetail.profile.defaultPresetId))
            const shared = (effective?.presetIds ?? []).some(id => id !== profileDetail.profile.defaultPresetId)
            return <label key={item.entityId}><input type="checkbox" checked={direct} onChange={event => void toggleDirectMod(item.entityId, event.target.checked)}/><Icon name={kindIcon(String(item.kind))} size={14}/><span><strong>{item.displayName}</strong><small>{item.linked ? item.archivePath : 'Source archive missing'}</small></span>{shared && <Badge tone="cyan">Via preset</Badge>}</label>
          })}</div></section>
        </>}
      </main>

      <aside className="preset-detail">
        {!presetDetail ? <EmptyState icon="mixed" title="Reusable presets" detail="Create a preset to reuse the same mod group across mod profiles."/> : <><header><div><span>REUSABLE PRESET</span><h2>{presetDetail.preset.name}</h2><p>{presetDetail.preset.description || 'No description'}</p></div><div><button className="icon-button" onClick={renamePreset} title="Edit preset"><Icon name="edit" size={14}/></button><button className="icon-button" onClick={deletePreset} title="Delete preset"><Icon name="trash" size={14}/></button></div></header><label className="search-box search-box--wide"><Icon name="search" size={13}/><input value={modQuery} onChange={event => setModQuery(event.target.value)} placeholder="Filter library"/></label><div className="membership-list membership-list--preset">{filteredItems.map(item => <label key={item.entityId}><input type="checkbox" checked={presetEntityIDs.includes(item.entityId)} onChange={event => void togglePresetMod(item.entityId, event.target.checked)}/><Icon name={kindIcon(String(item.kind))} size={14}/><span><strong>{item.displayName}</strong><small>{item.archivePath}</small></span></label>)}</div></>}
      </aside>
    </div>
  </section>
}
