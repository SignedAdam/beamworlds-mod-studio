const $ = (selector) => document.querySelector(selector);
const collator = new Intl.Collator(undefined, { sensitivity: 'base', numeric: true });
const dateFormatter = new Intl.DateTimeFormat(undefined, { year: 'numeric', month: '2-digit', day: '2-digit' });

const elements = {
  modList: $('#mod-list'),
  empty: $('#empty-state'),
  search: $('#search-input'),
  categoryFilters: $('#category-filters'),
  sourceFilters: $('#source-filters'),
  selectAll: $('#select-all'),
  bulkBar: $('#bulk-bar'),
  selectedCount: $('#selected-count'),
  detailDrawer: $('#detail-drawer'),
  detailBody: $('#detail-body'),
  detailEnabled: $('#detail-enabled'),
  dialog: $('#confirm-dialog'),
};

const ui = {
  data: null,
  view: 'all',
  category: '',
  source: '',
  sortKey: 'title',
  sortDirection: 'asc',
  selected: new Set(),
  detailId: null,
  pollTimer: null,
};

function make(tag, className, text) {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (text !== undefined && text !== null) element.textContent = text;
  return element;
}

function formatBytes(bytes) {
  if (!Number.isFinite(bytes)) return '—';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function formatDate(value) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? '—' : dateFormatter.format(date);
}

function healthLabel(health) {
  return ({
    healthy: 'Healthy',
    'needs-review': 'Needs review',
    invalid: 'Invalid',
    problematic: 'Problematic',
    missing: 'Missing',
  })[health] ?? health;
}

function sourceLabel(source) {
  return source === 'repository' ? 'Repository' : 'Third-party';
}

function effectiveCategory(mod) {
  return mod.categoryOverride || mod.autoCategory || 'Other';
}

async function request(path, options = {}) {
  const headers = { ...(options.headers ?? {}) };
  if (options.body && typeof options.body !== 'string') {
    headers['Content-Type'] = 'application/json';
    options.body = JSON.stringify(options.body);
  }
  if (options.method && options.method !== 'GET') headers['X-Mod-Manager-Token'] = ui.data?.token ?? '';
  const response = await fetch(path, { ...options, headers });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok && response.status !== 207) throw new Error(payload.error || `Request failed (${response.status})`);
  return payload;
}

function toast(message, kind = 'info') {
  const item = make('div', `toast ${kind}`, message);
  $('#toast-region').append(item);
  window.setTimeout(() => item.remove(), 4800);
}

async function loadState({ quiet = false } = {}) {
  try {
    ui.data = await request('/api/state');
    render();
    schedulePolling();
  } catch (error) {
    if (!quiet) toast(error.message, 'error');
    throw error;
  }
}

function schedulePolling() {
  if (ui.data?.scan.running && !ui.pollTimer) {
    ui.pollTimer = window.setInterval(() => loadState({ quiet: true }).catch(() => {}), 900);
  } else if (!ui.data?.scan.running && ui.pollTimer) {
    window.clearInterval(ui.pollTimer);
    ui.pollTimer = null;
  }
}

function renderStats() {
  const { stats, paths } = ui.data;
  $('#nav-total').textContent = stats.total.toLocaleString();
  $('#nav-enabled').textContent = stats.enabled.toLocaleString();
  $('#nav-disabled').textContent = stats.disabled.toLocaleString();
  $('#nav-review').textContent = (stats.needsReview + stats.invalid).toLocaleString();
  $('#nav-problematic').textContent = stats.problematic.toLocaleString();
  $('#library-size').textContent = formatBytes(stats.bytes);
  $('#library-path').textContent = paths.libraryDir;
  $('#library-path').title = paths.libraryDir;

  $('#stat-total').textContent = stats.total.toLocaleString();
  $('#stat-source').textContent = `${stats.repository.toLocaleString()} repository · ${stats.thirdParty.toLocaleString()} third-party`;
  $('#stat-enabled').textContent = stats.enabled.toLocaleString();
  $('#stat-review').textContent = (stats.needsReview + stats.invalid).toLocaleString();
  $('#stat-invalid').textContent = `${stats.invalid.toLocaleString()} structurally invalid`;
  $('#stat-duplicates').textContent = stats.duplicates.toLocaleString();

  const collect = $('#preserve-button');
  const movable = stats.enabled + stats.loose;
  collect.disabled = movable === 0;
  collect.textContent = movable === 0 ? 'Loose mods collected' : `Collect loose mods (${movable})`;
}

function renderScan() {
  const scan = ui.data.scan;
  const panel = $('#scan-panel');
  panel.classList.toggle('hidden', !scan.running && !scan.error);
  $('#scan-button').disabled = scan.running;
  $('#scan-button').textContent = scan.running ? 'Scanning…' : 'Rescan library';
  if (scan.error) {
    $('#scan-title').textContent = 'Scan failed';
    $('#scan-detail').textContent = scan.error;
    $('#scan-progress').style.width = '100%';
    return;
  }
  const percent = scan.total > 0 ? Math.round(scan.completed * 100 / scan.total) : 0;
  $('#scan-title').textContent = scan.phase === 'discover' ? 'Finding archives' : 'Inspecting archive metadata';
  $('#scan-detail').textContent = scan.total > 0 ? `${scan.completed.toLocaleString()} of ${scan.total.toLocaleString()} · ${scan.current || ''}` : 'Preparing scan…';
  $('#scan-progress').style.width = `${percent}%`;
}

function renderFacets() {
  const mods = ui.data.mods.filter((mod) => !mod.missing);
  const categoryCounts = new Map();
  for (const mod of mods) {
    const category = effectiveCategory(mod);
    categoryCounts.set(category, (categoryCounts.get(category) ?? 0) + 1);
  }

  const categories = [...categoryCounts.keys()].sort((a, b) => collator.compare(a, b));
  if (ui.category && !categoryCounts.has(ui.category)) ui.category = '';
  const fragment = document.createDocumentFragment();
  const addCategory = (label, value, count) => {
    const button = make('button', `facet-option${ui.category === value ? ' active' : ''}`);
    button.type = 'button';
    button.dataset.category = value;
    button.append(make('span', null, label), make('strong', null, count.toLocaleString()));
    button.addEventListener('click', () => {
      ui.category = value;
      renderFacets();
      renderModList();
    });
    fragment.append(button);
  };
  addCategory('All categories', '', mods.length);
  for (const category of categories) addCategory(category, category, categoryCounts.get(category));
  elements.categoryFilters.replaceChildren(fragment);

  $('#source-all-count').textContent = mods.length.toLocaleString();
  $('#source-repository-count').textContent = mods.filter((mod) => mod.source === 'repository').length.toLocaleString();
  $('#source-third-party-count').textContent = mods.filter((mod) => mod.source === 'third-party').length.toLocaleString();
  for (const button of elements.sourceFilters.querySelectorAll('[data-source]')) {
    button.classList.toggle('active', button.dataset.source === ui.source);
  }
}

const healthRank = { problematic: 0, invalid: 1, 'needs-review': 2, missing: 3, healthy: 4 };
const sortAccessors = {
  enabled: (mod) => Number(mod.enabled),
  title: (mod) => mod.title || mod.filename || '',
  filename: (mod) => mod.filename || '',
  category: (mod) => effectiveCategory(mod),
  source: (mod) => sourceLabel(mod.source),
  author: (mod) => mod.author || '',
  version: (mod) => mod.version || '',
  size: (mod) => Number(mod.size) || 0,
  modified: (mod) => Date.parse(mod.modifiedAt) || 0,
  health: (mod) => healthRank[mod.health] ?? Number.MAX_SAFE_INTEGER,
};

function compareByColumn(left, right) {
  const accessor = sortAccessors[ui.sortKey] ?? sortAccessors.title;
  const leftValue = accessor(left);
  const rightValue = accessor(right);
  let result;
  if (typeof leftValue === 'number' && typeof rightValue === 'number') result = leftValue - rightValue;
  else result = collator.compare(String(leftValue), String(rightValue));
  if (result === 0 && ui.sortKey !== 'title') result = collator.compare(left.title || left.filename, right.title || right.filename);
  return ui.sortDirection === 'desc' ? -result : result;
}

function visibleMods() {
  const query = elements.search.value.trim().toLocaleLowerCase();
  const filtered = ui.data.mods.filter((mod) => {
    if (mod.missing && ui.view !== 'review') return false;
    if (ui.view === 'enabled' && !mod.enabled) return false;
    if (ui.view === 'disabled' && mod.enabled) return false;
    if (ui.view === 'review' && !['needs-review', 'invalid', 'missing'].includes(mod.health)) return false;
    if (ui.view === 'problematic' && mod.health !== 'problematic') return false;
    if (ui.category && effectiveCategory(mod) !== ui.category) return false;
    if (ui.source && mod.source !== ui.source) return false;
    if (query) {
      const haystack = [
        mod.title, mod.filename, mod.author, mod.version, mod.archiveDescription,
        mod.description, mod.notes, effectiveCategory(mod), ...(mod.tags ?? []), ...(mod.contentTags ?? []),
      ].filter(Boolean).join('\n').toLocaleLowerCase();
      if (!haystack.includes(query)) return false;
    }
    return true;
  });
  filtered.sort(compareByColumn);
  return filtered;
}

function renderSortHeaders() {
  for (const heading of document.querySelectorAll('[data-sort-column]')) {
    const active = heading.dataset.sortColumn === ui.sortKey;
    heading.setAttribute('aria-sort', active ? (ui.sortDirection === 'asc' ? 'ascending' : 'descending') : 'none');
    const button = heading.querySelector('.column-sort');
    if (button) button.title = active
      ? `Sorted ${ui.sortDirection === 'asc' ? 'ascending' : 'descending'}; click to reverse`
      : `Sort by ${button.firstChild?.textContent ?? heading.dataset.sortColumn}`;
  }
}

function textCell(className, text, title = text) {
  const cell = make('td', className, text || '—');
  if (title) cell.title = title;
  return cell;
}

function renderModList() {
  const mods = visibleMods();
  const fragment = document.createDocumentFragment();
  for (const mod of mods) {
    const displayTitle = mod.title || mod.filename;
    const row = make('tr', `${ui.selected.has(mod.id) ? 'selected ' : ''}${ui.detailId === mod.id ? 'inspected' : ''}`.trim());
    row.dataset.id = mod.id;
    row.tabIndex = 0;
    row.title = 'Open mod inspector';
    row.addEventListener('click', (event) => {
      if (!event.target.closest('input, button')) openDetails(mod.id);
    });
    row.addEventListener('keydown', (event) => {
      if (event.key === 'Enter') openDetails(mod.id);
    });

    const selectionCell = make('td', 'selection-cell');
    const checkbox = make('input', 'row-select');
    checkbox.type = 'checkbox';
    checkbox.checked = ui.selected.has(mod.id);
    checkbox.ariaLabel = `Select ${displayTitle}`;
    checkbox.addEventListener('click', (event) => event.stopPropagation());
    checkbox.addEventListener('change', () => {
      checkbox.checked ? ui.selected.add(mod.id) : ui.selected.delete(mod.id);
      renderModList();
      renderBulkBar();
    });
    selectionCell.append(checkbox);

    const stateCell = make('td', 'state-cell');
    const state = make('input', 'state-check');
    state.type = 'checkbox';
    state.checked = mod.enabled;
    state.disabled = mod.missing;
    state.ariaLabel = `${mod.enabled ? 'Deactivate' : 'Activate'} ${displayTitle}`;
    state.title = mod.enabled ? 'Active in BeamNG; clear to deactivate' : 'Inactive; check to activate';
    state.addEventListener('click', (event) => event.stopPropagation());
    state.addEventListener('change', () => {
      state.disabled = true;
      toggleMod(mod);
    });
    stateCell.append(state);

    const title = textCell('mod-title-cell', displayTitle);
    const filename = textCell('filename-cell', mod.filename, mod.path);
    const category = textCell('category-cell', effectiveCategory(mod));
    const source = textCell('source-text', sourceLabel(mod.source));
    const author = textCell('author-cell', mod.author);
    const version = textCell('version-cell', mod.version);
    const size = textCell('numeric-cell', formatBytes(mod.size));
    const modified = textCell('modified-cell', formatDate(mod.modifiedAt), mod.modifiedAt);
    const health = textCell(`health-text ${mod.health}`, healthLabel(mod.health));
    row.append(selectionCell, stateCell, title, filename, category, source, author, version, size, modified, health);
    fragment.append(row);
  }

  elements.modList.replaceChildren(fragment);
  elements.empty.classList.toggle('hidden', mods.length > 0);
  $('#result-count').textContent = `${mods.length.toLocaleString()} ${mods.length === 1 ? 'mod' : 'mods'}`;
  elements.selectAll.checked = mods.length > 0 && mods.every((mod) => ui.selected.has(mod.id));
  elements.selectAll.indeterminate = mods.some((mod) => ui.selected.has(mod.id)) && !elements.selectAll.checked;
  renderSortHeaders();
}

function renderBulkBar() {
  for (const id of [...ui.selected]) {
    if (!ui.data.mods.some((mod) => mod.id === id && !mod.missing)) ui.selected.delete(id);
  }
  elements.bulkBar.classList.toggle('hidden', ui.selected.size === 0);
  elements.selectedCount.textContent = `${ui.selected.size.toLocaleString()} selected`;
}

function addFact(container, label, value) {
  const fact = make('div', 'detail-fact');
  fact.append(make('span', null, label), make('strong', null, value || '—'));
  container.append(fact);
}

function addSection(title) {
  const section = make('section', 'detail-section');
  section.append(make('h3', null, title));
  elements.detailBody.append(section);
  return section;
}

function addField(section, label, id, type, value, placeholder = '') {
  const wrapper = make('label', 'field');
  wrapper.append(make('span', null, label));
  const control = make(type === 'textarea' ? 'textarea' : 'input');
  control.id = id;
  control.value = value ?? '';
  control.placeholder = placeholder;
  wrapper.append(control);
  section.append(wrapper);
  return control;
}

function renderDetails() {
  const mod = ui.data.mods.find((candidate) => candidate.id === ui.detailId);
  if (!mod) return closeDetails();
  $('#detail-title').textContent = mod.title;
  $('#detail-filename').textContent = mod.filename;
  elements.detailBody.replaceChildren();

  const overview = addSection('Overview');
  const facts = make('div', 'detail-grid');
  addFact(facts, 'Category', effectiveCategory(mod));
  addFact(facts, 'Source', sourceLabel(mod.source));
  addFact(facts, 'Archive size', formatBytes(mod.size));
  addFact(facts, 'Contents', `${mod.entryCount.toLocaleString()} entries`);
  addFact(facts, 'Author', mod.author);
  addFact(facts, 'Version', mod.version);
  overview.append(facts);
  if (mod.archiveDescription) overview.append(make('p', 'path-block', mod.archiveDescription));
  overview.append(make('div', 'path-block', mod.path));

  const organization = addSection('Your organization');
  addField(organization, 'Category override', 'detail-category', 'input', mod.categoryOverride, `Automatic: ${mod.autoCategory}`);
  addField(organization, 'Tags (comma-separated)', 'detail-tags', 'input', (mod.tags ?? []).join(', '), 'favorite, paid, realistic…');
  addField(organization, 'Description', 'detail-description', 'textarea', mod.description, 'Your description of this mod');
  addField(organization, 'Private notes', 'detail-notes', 'textarea', mod.notes, 'Compatibility notes, dependencies, or reminders');
  const problematicLabel = make('label', 'problematic-field');
  const problematic = make('input');
  problematic.type = 'checkbox';
  problematic.id = 'detail-problematic';
  problematic.checked = mod.problematic;
  problematicLabel.append(problematic, make('span', null, 'Mark this mod as problematic'));
  organization.append(problematicLabel);
  if ((mod.contentTags ?? []).length > 0) {
    organization.append(make('div', 'content-tags', `Detected content: ${mod.contentTags.join(', ')}`));
  }

  const health = addSection('Health signals');
  const issues = make('div', 'issue-list');
  const healthSignals = [...(mod.issues ?? []), ...(mod.runtimeIssues ?? [])];
  if (healthSignals.length === 0) {
    const issue = make('div', 'issue');
    issue.append(make('strong', null, 'No structural issues'), make('span', null, 'The ZIP directory and recognized BeamNG content look valid.'));
    issues.append(issue);
  } else {
    for (const signal of healthSignals) {
      const issue = make('div', `issue ${signal.severity}`);
      issue.append(make('strong', null, signal.code.replaceAll('-', ' ')), make('span', null, signal.message));
      if (signal.examples?.length) issue.append(make('code', 'runtime-example', signal.examples[0]));
      issues.append(issue);
    }
  }
  health.append(issues);

  const metadata = addSection('Embedded metadata');
  if ((mod.metadataDocuments ?? []).length === 0 && !mod.database) {
    metadata.append(make('p', 'cell-muted', 'No readable metadata document was found.'));
  }
  if (mod.database) {
    const details = make('details', 'metadata-document');
    details.append(make('summary', null, 'BeamNG repository database'));
    const pre = make('pre');
    pre.textContent = JSON.stringify(mod.database, null, 2);
    details.append(pre);
    metadata.append(details);
  }
  for (const document of mod.metadataDocuments ?? []) {
    const details = make('details', 'metadata-document');
    details.append(make('summary', null, document.path));
    const pre = make('pre');
    pre.textContent = JSON.stringify(document.data, null, 2);
    details.append(pre);
    metadata.append(details);
  }

  elements.detailEnabled.checked = mod.enabled;
  elements.detailEnabled.disabled = mod.missing;
  $('#detail-reveal').disabled = mod.missing;
}

function openDetails(id) {
  ui.detailId = id;
  renderDetails();
  elements.detailDrawer.classList.add('open');
  elements.detailDrawer.setAttribute('aria-hidden', 'false');
  renderModList();
}

function closeDetails() {
  ui.detailId = null;
  elements.detailDrawer.classList.remove('open');
  elements.detailDrawer.setAttribute('aria-hidden', 'true');
  renderModList();
}

async function toggleMod(mod) {
  try {
    await request(`/api/mods/${mod.id}/${mod.enabled ? 'disable' : 'enable'}`, { method: 'POST' });
    toast(`${mod.title} ${mod.enabled ? 'disabled' : 'enabled'}.`);
    await loadState();
    if (ui.detailId === mod.id) renderDetails();
  } catch (error) {
    toast(error.message, 'error');
  }
}

async function bulkToggle(enabled) {
  if (ui.selected.size === 0) return;
  try {
    const result = await request('/api/mods/bulk', { method: 'POST', body: { ids: [...ui.selected], enabled } });
    toast(`${result.moved} ${result.moved === 1 ? 'mod' : 'mods'} ${enabled ? 'enabled' : 'disabled'}.${result.errors.length ? ` ${result.errors.length} failed.` : ''}`, result.errors.length ? 'error' : 'info');
    ui.selected.clear();
    await loadState();
  } catch (error) {
    toast(error.message, 'error');
  }
}

async function saveDetails() {
  const mod = ui.data.mods.find((candidate) => candidate.id === ui.detailId);
  if (!mod) return;
  const category = $('#detail-category').value.trim();
  try {
    await request(`/api/mods/${mod.id}`, {
      method: 'PATCH',
      body: {
        categoryOverride: category || null,
        tags: $('#detail-tags').value.split(',').map((tag) => tag.trim()).filter(Boolean),
        description: $('#detail-description').value,
        notes: $('#detail-notes').value,
        problematic: $('#detail-problematic').checked,
      },
    });
    toast('Mod details saved.');
    await loadState();
    renderDetails();
  } catch (error) {
    toast(error.message, 'error');
  }
}

function render() {
  renderStats();
  renderScan();
  renderFacets();
  renderModList();
  renderBulkBar();
  if (ui.detailId) renderDetails();
}

for (const button of document.querySelectorAll('[data-view]')) {
  button.addEventListener('click', () => {
    document.querySelector('[data-view].active')?.classList.remove('active');
    button.classList.add('active');
    ui.view = button.dataset.view;
    renderModList();
  });
}
for (const button of elements.sourceFilters.querySelectorAll('[data-source]')) {
  button.addEventListener('click', () => {
    ui.source = button.dataset.source;
    renderFacets();
    renderModList();
  });
}
for (const button of document.querySelectorAll('.column-sort')) {
  button.addEventListener('click', () => {
    const key = button.dataset.sort;
    if (ui.sortKey === key) ui.sortDirection = ui.sortDirection === 'asc' ? 'desc' : 'asc';
    else {
      ui.sortKey = key;
      ui.sortDirection = 'asc';
    }
    renderModList();
  });
}
elements.search.addEventListener('input', renderModList);

elements.selectAll.addEventListener('change', () => {
  for (const mod of visibleMods()) {
    if (elements.selectAll.checked) ui.selected.add(mod.id);
    else ui.selected.delete(mod.id);
  }
  renderModList();
  renderBulkBar();
});
$('#clear-selection').addEventListener('click', () => { ui.selected.clear(); renderModList(); renderBulkBar(); });
$('#bulk-enable').addEventListener('click', () => bulkToggle(true));
$('#bulk-disable').addEventListener('click', () => bulkToggle(false));
$('#drawer-close').addEventListener('click', closeDetails);
$('#detail-save').addEventListener('click', saveDetails);
elements.detailEnabled.addEventListener('change', () => {
  const mod = ui.data.mods.find((candidate) => candidate.id === ui.detailId);
  if (mod && mod.enabled !== elements.detailEnabled.checked) toggleMod(mod);
});
$('#detail-reveal').addEventListener('click', async () => {
  try { await request('/api/reveal', { method: 'POST', body: { id: ui.detailId } }); }
  catch (error) { toast(error.message, 'error'); }
});
$('#scan-button').addEventListener('click', async () => {
  try {
    await request('/api/scan', { method: 'POST' });
    await loadState();
  } catch (error) { toast(error.message, 'error'); }
});
$('#diagnose-button').addEventListener('click', async () => {
  const button = $('#diagnose-button');
  button.disabled = true;
  button.textContent = 'Analyzing…';
  try {
    const result = await request('/api/diagnose', { method: 'POST' });
    toast(`Analyzed ${result.diagnostics.errorLines.toLocaleString()} error lines from ${result.diagnostics.logs.length} logs.`);
    await loadState();
  } catch (error) {
    toast(error.message, 'error');
  } finally {
    button.disabled = false;
    button.textContent = 'Analyze game logs';
  }
});
$('#preserve-button').addEventListener('click', () => {
  const movable = ui.data.stats.enabled + ui.data.stats.loose;
  $('#confirm-copy').textContent = `${movable.toLocaleString()} archives will be moved into the managed library. No archive will be deleted. Enabled mods will leave BeamNG until you enable them here again.`;
  elements.dialog.showModal();
});
elements.dialog.addEventListener('close', async () => {
  if (elements.dialog.returnValue !== 'confirm') return;
  $('#preserve-button').disabled = true;
  try {
    const result = await request('/api/preserve', { method: 'POST' });
    toast(`Collected ${result.moved} archives.${result.errors.length ? ` ${result.errors.length} failed.` : ''}`, result.errors.length ? 'error' : 'info');
    await loadState();
  } catch (error) { toast(error.message, 'error'); }
});
window.addEventListener('keydown', (event) => { if (event.key === 'Escape' && ui.detailId) closeDetails(); });

loadState().catch(() => {});
