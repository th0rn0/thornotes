/* thornotes — main app */
'use strict';

// ── Theme ──────────────────────────────────────────────────────────────────
(function initTheme() {
  const saved = localStorage.getItem('theme');
  const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
  const isDark = saved === 'dark' || (!saved && prefersDark);
  if (isDark) {
    document.body.classList.add('dark');
    const hljsTheme = document.getElementById('hljs-theme');
    if (hljsTheme) hljsTheme.href = '/static/css/highlight-github-dark.min.css';
  }
})();

// ── State ──────────────────────────────────────────────────────────────────
let csrfToken = '';
let currentUser = null;
let currentNote = null;      // { id, title, content_hash, disk_path, folder_id, tags }
let currentFolderId = null;  // highlighted folder in the tree
let editor = null;           // EasyMDE instance
let saveTimer = null;
let loadedFolderIds = new Set(); // tracks which folders have had their notes loaded
let folders = [];            // flat folder list from API
let notesByFolder = {};      // { folderId: [noteListItem] }
let rootNotes = [];          // notes with no folder
let searchResults = null;    // null = not in search mode
let journals = [];           // all journals for current user

const AUTO_SAVE_DELAY_MS = 1500;

// ── Init ───────────────────────────────────────────────────────────────────
(async function init() {
  try {
    const me = await api('GET', '/api/v1/auth/me');
    currentUser = me;
    document.getElementById('topbar-username').textContent = me.username;
    const csrf = await api('GET', '/api/v1/csrf');
    csrfToken = csrf.csrf_token;
    await Promise.all([loadFolderTree(), loadJournals()]);
    showApp();
  } catch {
    showAuth();
  }
})();

// ── Auth ───────────────────────────────────────────────────────────────────
function showLogin() {
  document.getElementById('auth-form-login').style.display = '';
  document.getElementById('auth-form-register').style.display = 'none';
  document.getElementById('login-error').textContent = '';
}

function showRegister() {
  document.getElementById('auth-form-login').style.display = 'none';
  document.getElementById('auth-form-register').style.display = '';
  document.getElementById('reg-error').textContent = '';
}

async function login() {
  const username = document.getElementById('login-username').value.trim();
  const password = document.getElementById('login-password').value;
  document.getElementById('login-error').textContent = '';
  try {
    const res = await api('POST', '/api/v1/auth/login', { username, password });
    csrfToken = res.csrf_token;
    const me = await api('GET', '/api/v1/auth/me');
    currentUser = me;
    document.getElementById('topbar-username').textContent = me.username;
    await Promise.all([loadFolderTree(), loadJournals()]);
    showApp();
  } catch (e) {
    document.getElementById('login-error').textContent = e.message || 'Login failed';
  }
}

async function register() {
  const username = document.getElementById('reg-username').value.trim();
  const password = document.getElementById('reg-password').value;
  document.getElementById('reg-error').textContent = '';
  try {
    await api('POST', '/api/v1/auth/register', { username, password });
    document.getElementById('login-username').value = username;
    document.getElementById('login-password').value = password;
    showLogin();
    await login();
  } catch (e) {
    document.getElementById('reg-error').textContent = e.message || 'Registration failed';
  }
}

async function logout() {
  await api('POST', '/api/v1/auth/logout').catch(() => {});
  location.reload();
}

function showAuth() {
  document.getElementById('auth-screen').style.display = 'flex';
  document.getElementById('app-screen').style.display = 'none';
}

function showApp() {
  document.getElementById('auth-screen').style.display = 'none';
  document.getElementById('app-screen').style.display = 'flex';
  document.getElementById('dark-mode-toggle').checked = document.body.classList.contains('dark');
  connectEventSource();
}

// ── Mobile sidebar ─────────────────────────────────────────────────────────
function toggleSidebar() {
  const sidebar = document.getElementById('sidebar');
  const overlay = document.getElementById('sidebar-overlay');
  const isOpen = sidebar.classList.contains('mobile-open');
  sidebar.classList.toggle('mobile-open', !isOpen);
  overlay.classList.toggle('active', !isOpen);
}

function closeSidebar() {
  document.getElementById('sidebar').classList.remove('mobile-open');
  document.getElementById('sidebar-overlay').classList.remove('active');
}

function isMobile() {
  return window.matchMedia('(max-width: 640px)').matches;
}

// ── Disk-change SSE ────────────────────────────────────────────────────────
let _sse = null;
let _sseReconnectTimer = null;

function connectEventSource() {
  if (_sse) return; // already connected
  _sse = new EventSource('/api/v1/events', { withCredentials: true });

  _sse.addEventListener('notes_changed', async () => {
    // Reload the folder tree so counts and note titles stay fresh.
    await loadFolderTree().catch(() => {});
    // If the currently open note was changed on disk, reload its content.
    if (currentNote) {
      try {
        const fresh = await api('GET', `/api/v1/notes/${currentNote.id}`);
        if (fresh.content_hash !== currentNote.content_hash) {
          // Only overwrite if the user has no unsaved edits (save status is 'saved').
          const saveStatus = document.getElementById('save-status');
          if (saveStatus && saveStatus.classList.contains('saved')) {
            currentNote = fresh;
            editor && editor.value(fresh.content);
            document.getElementById('note-title').value = fresh.title;
            document.getElementById('note-tags').value = (fresh.tags || []).join(', ');
            document.getElementById('note-stats').textContent = `${fresh.content.length} chars`;
          }
        }
      } catch { /* note may have been deleted */ }
    }
  });

  _sse.onerror = () => {
    _sse.close();
    _sse = null;
    // Reconnect with exponential backoff (5s, then browser handles further retries).
    clearTimeout(_sseReconnectTimer);
    _sseReconnectTimer = setTimeout(connectEventSource, 5000);
  };
}

// ── Folder tree ────────────────────────────────────────────────────────────
async function loadFolderTree() {
  [folders, rootNotes] = await Promise.all([
    api('GET', '/api/v1/folders'),
    api('GET', '/api/v1/notes/root'),
  ]);
  renderTree();
}

async function loadFolderNotes(folderId) {
  if (loadedFolderIds.has(folderId)) return;
  const items = await api('GET', `/api/v1/folders/${folderId}/notes`);
  notesByFolder[folderId] = items || [];
  loadedFolderIds.add(folderId);
  renderTree();
}

function renderTree() {
  const tree = document.getElementById('tree');
  if (searchResults !== null) {
    renderSearchResults();
    return;
  }

  const roots = folders.filter(f => !f.parent_id);
  let html = '';

  function renderFolder(f, depth = 0) {
    const indent = depth * 8;
    const expanded = loadedFolderIds.has(f.id);
    const icon = expanded ? '▾' : '▸';
    const notes = notesByFolder[f.id] || [];
    const children = folders.filter(c => c.parent_id === f.id);

    const folderActive = currentFolderId === f.id ? ' active' : '';
    html += `<div class="tree-folder" data-folder-id="${f.id}">`;
    html += `<div class="tree-folder-label${folderActive}" style="padding-left:${8 + indent}px" data-action="select-folder" data-folder-id="${f.id}">`;
    html += `<span class="icon">${icon}</span>${esc(f.name)}`;
    html += `</div>`;

    if (expanded) {
      html += `<div class="tree-notes">`;
      for (const n of notes) {
        const active = currentNote && currentNote.id === n.id ? ' active' : '';
        html += `<div class="tree-note${active}" style="padding-left:${20 + indent}px" data-action="open-note" data-note-id="${n.id}" title="${esc(n.title)}">${esc(n.title)}</div>`;
      }
      for (const child of children) {
        renderFolder(child, depth + 1);
      }
      html += `</div>`;
    }

    html += `</div>`;
  }

  for (const f of roots) renderFolder(f);

  // Root (unsorted) notes.
  if (rootNotes.length > 0) {
    html += `<div class="tree-unsorted">Unsorted</div>`;
    for (const n of rootNotes) {
      const active = currentNote && currentNote.id === n.id ? ' active' : '';
      html += `<div class="tree-note${active}" style="padding-left:12px" data-action="open-note" data-note-id="${n.id}" title="${esc(n.title)}">${esc(n.title)}</div>`;
    }
  }

  tree.innerHTML = html;
}

async function selectFolder(folderId) {
  currentFolderId = folderId;
  if (loadedFolderIds.has(folderId)) {
    loadedFolderIds.delete(folderId);
    renderTree();
  } else {
    await loadFolderNotes(folderId);
  }
}

// ── Search ─────────────────────────────────────────────────────────────────
let searchDebounce = null;

function onSearch(q) {
  clearTimeout(searchDebounce);
  if (!q) {
    searchResults = null;
    renderTree();
    return;
  }
  searchDebounce = setTimeout(async () => {
    try {
      searchResults = await api('GET', `/api/v1/notes?q=${encodeURIComponent(q)}`);
      renderTree();
    } catch { /* ignore */ }
  }, 300);
}

function renderSearchResults() {
  const tree = document.getElementById('tree');
  if (!searchResults || searchResults.length === 0) {
    tree.innerHTML = '<div style="padding:12px; font-size:12px; color:#aaa;">No results</div>';
    return;
  }
  let html = '';
  for (const r of searchResults) {
    const active = currentNote && currentNote.id === r.note_id ? ' active' : '';
    html += `<div class="tree-note${active}" data-action="open-note" data-note-id="${r.note_id}" title="${esc(r.title)}">${esc(r.title)}</div>`;
    if (r.snippet) {
      html += `<div style="padding:2px 16px 6px; font-size:11px; color:#888; white-space:nowrap; overflow:hidden; text-overflow:ellipsis;">${esc(r.snippet)}</div>`;
    }
  }
  tree.innerHTML = html;
}

// ── Note ops ───────────────────────────────────────────────────────────────
async function openNote(noteId) {
  const note = await api('GET', `/api/v1/notes/${noteId}`);
  currentNote = note;
  if (isMobile()) closeSidebar();

  document.getElementById('empty-state').style.display = 'none';
  const container = document.getElementById('editor-container');
  container.style.display = 'flex';

  document.getElementById('note-title').value = note.title;
  document.getElementById('note-tags').value = (note.tags || []).join(', ');
  document.getElementById('note-path').textContent = note.disk_path;
  document.getElementById('note-stats').textContent = `${note.content.length} chars`;
  setSaveStatus('saved');

  if (!editor) {
    const editorArea = document.getElementById('editor-area');
    const ta = document.createElement('textarea');
    editorArea.appendChild(ta);
    editor = new EasyMDE({
      element: ta,
      autosave: { enabled: false },
      autoDownloadFontAwesome: false,
      toolbar: ['bold', 'italic', 'heading', '|', 'quote', 'unordered-list', 'ordered-list', '|',
        'link', 'image', '|', 'preview', 'side-by-side', 'fullscreen', '|', 'guide'],
      spellChecker: false,
      status: false,
      previewRender(text) {
        const html = marked.parse(text);
        const tmp = document.createElement('div');
        tmp.innerHTML = html;
        tmp.querySelectorAll('pre code').forEach(el => hljs.highlightElement(el));
        return tmp.innerHTML;
      },
    });
    editor.codemirror.on('change', onEditorChange);
  }

  editor.value(note.content);
  renderTree(); // refresh active state
}

function onEditorChange() {
  setSaveStatus('saving');
  clearTimeout(saveTimer);
  saveTimer = setTimeout(autoSave, AUTO_SAVE_DELAY_MS);
  const content = editor.value();
  document.getElementById('note-stats').textContent = `${content.length} chars`;
}

async function autoSave() {
  if (!currentNote) return;
  const content = editor.value();
  try {
    const res = await api('PATCH', `/api/v1/notes/${currentNote.id}`, {
      content,
      content_hash: currentNote.content_hash,
    });
    currentNote.content_hash = res.content_hash;
    setSaveStatus('saved');
  } catch (e) {
    if (e.status === 409) {
      showConflictModal();
    } else if (e.status === 507) {
      setSaveStatus('error');
      document.getElementById('disk-full-banner').style.display = 'block';
    } else {
      setSaveStatus('error');
    }
  }
}

function setSaveStatus(state) {
  const el = document.getElementById('save-status');
  el.className = 'save-status ' + state;
  el.textContent = state === 'saving' ? 'Saving…' : state === 'saved' ? 'Saved' : 'Error';
}

async function onTitleChange() {
  if (!currentNote) return;
  const title = document.getElementById('note-title').value;
  await api('PATCH', `/api/v1/notes/${currentNote.id}`, { title }).catch(() => {});
  currentNote.title = title;
  renderTree();
}

async function onTagsChange() {
  if (!currentNote) return;
  const raw = document.getElementById('note-tags').value;
  const tags = raw.split(',').map(t => t.trim()).filter(Boolean);
  await api('PATCH', `/api/v1/notes/${currentNote.id}`, { tags }).catch(() => {});
  currentNote.tags = tags;
}

function promptCreateNote() {
  const sel = document.getElementById('new-note-folder');
  sel.innerHTML = '<option value="">Unsorted</option>';
  for (const f of folders) {
    const opt = document.createElement('option');
    opt.value = f.id;
    opt.textContent = f.name;
    sel.appendChild(opt);
  }
  sel.value = currentFolderId || '';
  document.getElementById('new-note-title').value = '';
  document.getElementById('new-note-error').textContent = '';
  document.getElementById('new-note-modal').style.display = 'flex';
  document.getElementById('new-note-title').focus();
}

function closeNewNoteModal() {
  document.getElementById('new-note-modal').style.display = 'none';
}

async function submitNewNote() {
  const title = document.getElementById('new-note-title').value.trim();
  if (!title) {
    document.getElementById('new-note-error').textContent = 'Title is required.';
    return;
  }
  const folderVal = document.getElementById('new-note-folder').value;
  const folderId = folderVal ? parseInt(folderVal) : null;
  try {
    const note = await api('POST', '/api/v1/notes', { title, folder_id: folderId, tags: [] });
    closeNewNoteModal();
    if (folderId) {
      loadedFolderIds.delete(folderId);
      await loadFolderNotes(folderId);
    } else {
      rootNotes = await api('GET', '/api/v1/notes/root').catch(() => rootNotes);
    }
    await openNote(note.id);
  } catch (e) {
    document.getElementById('new-note-error').textContent = e.message || 'Failed to create note.';
  }
}

function promptCreateFolder() {
  const sel = document.getElementById('new-folder-parent');
  sel.innerHTML = '<option value="">Root (no parent)</option>';
  for (const f of folders) {
    const opt = document.createElement('option');
    opt.value = f.id;
    opt.textContent = f.name;
    sel.appendChild(opt);
  }
  sel.value = currentFolderId || '';
  document.getElementById('new-folder-name').value = '';
  document.getElementById('new-folder-error').textContent = '';
  document.getElementById('new-folder-modal').style.display = 'flex';
  document.getElementById('new-folder-name').focus();
}

function closeNewFolderModal() {
  document.getElementById('new-folder-modal').style.display = 'none';
}

async function submitNewFolder() {
  const name = document.getElementById('new-folder-name').value.trim();
  if (!name) {
    document.getElementById('new-folder-error').textContent = 'Name is required.';
    return;
  }
  const parentVal = document.getElementById('new-folder-parent').value;
  const parentId = parentVal ? parseInt(parentVal) : null;
  try {
    await api('POST', '/api/v1/folders', { name, parent_id: parentId });
    closeNewFolderModal();
    await loadFolderTree();
  } catch (e) {
    document.getElementById('new-folder-error').textContent = e.message || 'Failed to create folder.';
  }
}

async function copyToClipboard(text) {
  if (navigator.clipboard && navigator.clipboard.writeText) {
    await navigator.clipboard.writeText(text);
    return;
  }
  // Fallback for HTTP (non-secure) contexts where navigator.clipboard is unavailable
  const el = document.createElement('textarea');
  el.value = text;
  el.style.cssText = 'position:fixed;top:0;left:0;opacity:0;pointer-events:none';
  document.body.appendChild(el);
  el.select();
  document.execCommand('copy');
  document.body.removeChild(el);
}

async function shareNote() {
  if (!currentNote) return;
  const res = await api('POST', `/api/v1/notes/${currentNote.id}/share`, {});
  const url = `${location.origin}/s/${res.share_token}`;
  await copyToClipboard(url).catch(() => {});
  showNotification(`Share link copied: ${url}`);
}

// ── Conflict resolution ─────────────────────────────────────────────────────
function showConflictModal() {
  document.getElementById('conflict-modal').style.display = 'flex';
}

async function resolveConflict(action) {
  document.getElementById('conflict-modal').style.display = 'none';
  if (!currentNote) return;

  if (action === 'discard') {
    // Reload from server.
    await openNote(currentNote.id);
  } else {
    // Overwrite: fetch current server hash then force-save.
    const serverNote = await api('GET', `/api/v1/notes/${currentNote.id}`);
    currentNote.content_hash = serverNote.content_hash;
    await autoSave();
  }
}

// ── Notifications ──────────────────────────────────────────────────────────
let notifTimer = null;

function showNotification(msg, isError = false) {
  const el = document.getElementById('notification');
  el.textContent = msg;
  el.className = isError ? 'error' : '';
  el.style.display = 'block';
  clearTimeout(notifTimer);
  notifTimer = setTimeout(() => { el.style.display = 'none'; }, 4000);
}

// ── API helper ─────────────────────────────────────────────────────────────
async function api(method, path, body) {
  const opts = {
    method,
    headers: { 'Content-Type': 'application/json' },
    credentials: 'same-origin',
  };
  if (csrfToken && method !== 'GET' && method !== 'HEAD') {
    opts.headers['X-CSRF-Token'] = csrfToken;
  }
  if (body !== undefined) {
    opts.body = JSON.stringify(body);
  }

  const res = await fetch(path, opts);
  let data;
  try { data = await res.json(); } catch { data = {}; }

  if (!res.ok) {
    const err = new Error(data.error || `HTTP ${res.status}`);
    err.status = res.status;
    throw err;
  }
  return data;
}

// ── Dark mode ──────────────────────────────────────────────────────────────
function toggleDarkMode(dark) {
  document.body.classList.toggle('dark', dark);
  localStorage.setItem('theme', dark ? 'dark' : 'light');
  const hljsTheme = document.getElementById('hljs-theme');
  if (hljsTheme) {
    hljsTheme.href = dark
      ? '/static/css/highlight-github-dark.min.css'
      : '/static/css/highlight-github.min.css';
  }
}

// ── Account / API tokens ───────────────────────────────────────────────────
let _newTokenValue = '';

async function showAccountModal() {
  document.getElementById('mcp-endpoint').textContent = location.origin + '/mcp';
  document.getElementById('token-reveal-area').style.display = 'none';
  document.getElementById('new-token-name').value = '';
  _newTokenValue = '';
  await refreshTokenList();
  document.getElementById('account-modal').style.display = 'flex';
}

function closeAccountModal() {
  document.getElementById('account-modal').style.display = 'none';
}

async function refreshTokenList() {
  const tokens = await api('GET', '/api/v1/account/tokens').catch(() => []);
  const el = document.getElementById('token-list');
  if (!tokens || tokens.length === 0) {
    el.innerHTML = '<div style="font-size:12px;color:#aaa;padding:6px 0;">No tokens yet.</div>';
    return;
  }
  let html = '';
  for (const t of tokens) {
    const created = new Date(t.created_at).toLocaleDateString();
    const used = t.last_used_at ? new Date(t.last_used_at).toLocaleDateString() : 'never';
    html += `<div class="token-item">
      <span class="token-name">${esc(t.name)}</span>
      <span class="token-prefix" title="Token prefix">${esc(t.prefix)}…</span>
      <span class="token-date">created ${created} · used ${used}</span>
      <button class="token-revoke" data-action="revoke-token" data-token-id="${t.id}">Revoke</button>
    </div>`;
  }
  el.innerHTML = html;
}

async function createToken() {
  const name = document.getElementById('new-token-name').value.trim() || 'Default';
  try {
    const token = await api('POST', '/api/v1/account/tokens', { name });
    _newTokenValue = token.token;
    document.getElementById('token-reveal-value').textContent = token.token;
    document.getElementById('token-reveal-area').style.display = '';
    document.getElementById('new-token-name').value = '';
    await refreshTokenList();
  } catch (e) {
    showNotification(e.message || 'Failed to create token', true);
  }
}

async function revokeToken(id) {
  if (!confirm('Revoke this token? Any clients using it will lose access.')) return;
  await api('DELETE', `/api/v1/account/tokens/${id}`).catch(() => {});
  if (_newTokenValue) {
    document.getElementById('token-reveal-area').style.display = 'none';
    _newTokenValue = '';
  }
  await refreshTokenList();
}

async function copyNewToken() {
  if (!_newTokenValue) return;
  await copyToClipboard(_newTokenValue).catch(() => {});
  showNotification('Token copied to clipboard');
}

// ── Journals ───────────────────────────────────────────────────────────────
async function loadJournals() {
  try {
    journals = await api('GET', '/api/v1/journals');
  } catch {
    journals = [];
  }
  renderJournalSection();
}

function renderJournalSection() {
  const section = document.getElementById('journal-section');
  const picker = document.getElementById('journal-picker');
  const sel = document.getElementById('journal-select');

  if (!section) return;

  if (journals.length === 0) {
    // Show the section with just the manage button (prompts creation).
    section.style.display = '';
    picker.style.display = 'none';
    return;
  }

  section.style.display = '';

  if (journals.length > 1) {
    sel.innerHTML = journals.map(j => `<option value="${j.id}">${esc(j.name)}</option>`).join('');
    picker.style.display = '';
  } else {
    picker.style.display = 'none';
  }
}

async function openTodayJournal() {
  if (journals.length === 0) {
    showManageJournalsModal();
    return;
  }

  let journalId;
  if (journals.length === 1) {
    journalId = journals[0].id;
  } else {
    const sel = document.getElementById('journal-select');
    journalId = parseInt(sel.value, 10);
  }

  try {
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
    const note = await api('GET', `/api/v1/journals/${journalId}/today?tz=${encodeURIComponent(tz)}`);
    // Ensure the note's folder hierarchy is loaded in the tree.
    if (note.folder_id) {
      await ensureFolderLoaded(note.folder_id);
    }
    await openNote(note.id);
  } catch (e) {
    showNotification(e.message || 'Could not open today\'s journal entry', true);
  }
}

// ensureFolderLoaded walks up the folder tree to make sure all ancestor folders
// are expanded so the note is visible in the sidebar.
async function ensureFolderLoaded(folderId) {
  // Find all ancestor folder IDs.
  const ancestors = [];
  let id = folderId;
  while (id) {
    const f = folders.find(f => f.id === id);
    if (!f) break;
    ancestors.unshift(id);
    id = f.parent_id;
  }
  // Load notes for each ancestor folder (innermost last so tree renders correctly).
  for (const aid of ancestors) {
    if (!loadedFolderIds.has(aid)) {
      const items = await api('GET', `/api/v1/folders/${aid}/notes`).catch(() => []);
      notesByFolder[aid] = items || [];
      loadedFolderIds.add(aid);
    }
  }
}

function showManageJournalsModal() {
  document.getElementById('journal-error').textContent = '';
  document.getElementById('new-journal-name').value = '';
  renderJournalList();
  document.getElementById('manage-journals-modal').style.display = 'flex';
}

function closeManageJournalsModal() {
  document.getElementById('manage-journals-modal').style.display = 'none';
}

function renderJournalList() {
  const list = document.getElementById('journal-list');
  if (journals.length === 0) {
    list.innerHTML = '<p style="font-size:12px;color:#aaa;padding:4px 0">No journals yet. Create one below.</p>';
    return;
  }
  list.innerHTML = journals.map(j =>
    `<div class="journal-item">
       <span class="journal-item-name">${esc(j.name)}</span>
       <button class="journal-delete-btn" data-journal-id="${j.id}">Remove</button>
     </div>`
  ).join('');
}

async function submitNewJournal() {
  const name = document.getElementById('new-journal-name').value.trim();
  document.getElementById('journal-error').textContent = '';
  if (!name) return;
  try {
    const journal = await api('POST', '/api/v1/journals', { name });
    journals.push(journal);
    document.getElementById('new-journal-name').value = '';
    renderJournalList();
    renderJournalSection();
    // Reload folder tree so the new journal root folder appears.
    await loadFolderTree();
  } catch (e) {
    document.getElementById('journal-error').textContent = e.message || 'Failed to create journal';
  }
}

async function deleteJournal(id) {
  if (!confirm('Remove this journal? Your notes and folders will be kept.')) return;
  try {
    await api('DELETE', `/api/v1/journals/${id}`);
    journals = journals.filter(j => j.id !== id);
    renderJournalList();
    renderJournalSection();
  } catch (e) {
    showNotification(e.message || 'Failed to remove journal', true);
  }
}

// ── Utils ──────────────────────────────────────────────────────────────────
function esc(s) {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('/sw.js').catch(() => {});
}

// ── Event bindings (replaces inline onclick/onchange/oninput attrs) ─────────
// Auth
document.getElementById('login-btn').addEventListener('click', login);
document.getElementById('show-register-link').addEventListener('click', showRegister);
document.getElementById('register-btn').addEventListener('click', register);
document.getElementById('show-login-link').addEventListener('click', showLogin);

// Topbar
document.querySelector('.topbar-menu-btn').addEventListener('click', toggleSidebar);
document.getElementById('dark-mode-toggle').addEventListener('change', function() { toggleDarkMode(this.checked); });
document.getElementById('account-btn').addEventListener('click', showAccountModal);
document.getElementById('logout-btn').addEventListener('click', logout);

// Sidebar
document.getElementById('sidebar-overlay').addEventListener('click', closeSidebar);
document.getElementById('create-note-btn').addEventListener('click', promptCreateNote);
document.getElementById('create-folder-btn').addEventListener('click', promptCreateFolder);
document.getElementById('search-input').addEventListener('input', function() { onSearch(this.value); });
document.querySelector('.journal-today-btn').addEventListener('click', openTodayJournal);
document.querySelector('.journal-manage-btn').addEventListener('click', showManageJournalsModal);

// Editor
document.getElementById('note-title').addEventListener('change', onTitleChange);
document.getElementById('note-tags').addEventListener('change', onTagsChange);
document.querySelector('.share-btn').addEventListener('click', shareNote);

// Conflict modal
document.getElementById('conflict-discard-btn').addEventListener('click', function() { resolveConflict('discard'); });
document.getElementById('conflict-overwrite-btn').addEventListener('click', function() { resolveConflict('overwrite'); });

// New note modal
document.getElementById('new-note-modal').addEventListener('click', function(e) { if (e.target === this) closeNewNoteModal(); });
document.getElementById('new-note-title').addEventListener('keydown', function(e) {
  if (e.key === 'Enter') submitNewNote();
  if (e.key === 'Escape') closeNewNoteModal();
});
document.getElementById('new-note-folder').addEventListener('keydown', function(e) { if (e.key === 'Escape') closeNewNoteModal(); });
document.getElementById('new-note-cancel-btn').addEventListener('click', closeNewNoteModal);
document.getElementById('new-note-submit-btn').addEventListener('click', submitNewNote);

// New folder modal
document.getElementById('new-folder-modal').addEventListener('click', function(e) { if (e.target === this) closeNewFolderModal(); });
document.getElementById('new-folder-name').addEventListener('keydown', function(e) {
  if (e.key === 'Enter') submitNewFolder();
  if (e.key === 'Escape') closeNewFolderModal();
});
document.getElementById('new-folder-parent').addEventListener('keydown', function(e) { if (e.key === 'Escape') closeNewFolderModal(); });
document.getElementById('new-folder-cancel-btn').addEventListener('click', closeNewFolderModal);
document.getElementById('new-folder-submit-btn').addEventListener('click', submitNewFolder);

// Account modal
document.getElementById('account-modal').addEventListener('click', function(e) { if (e.target === this) closeAccountModal(); });
document.querySelector('.token-copy-btn').addEventListener('click', copyNewToken);
document.getElementById('create-token-btn').addEventListener('click', createToken);
document.getElementById('account-done-btn').addEventListener('click', closeAccountModal);

// Manage journals modal
document.getElementById('manage-journals-modal').addEventListener('click', function(e) { if (e.target === this) closeManageJournalsModal(); });
document.getElementById('new-journal-name').addEventListener('keydown', function(e) {
  if (e.key === 'Enter') submitNewJournal();
  if (e.key === 'Escape') closeManageJournalsModal();
});
document.getElementById('add-journal-btn').addEventListener('click', submitNewJournal);
document.getElementById('manage-journals-done-btn').addEventListener('click', closeManageJournalsModal);

// Journal list — event delegation for dynamically rendered delete buttons
document.getElementById('journal-list').addEventListener('click', function(e) {
  const btn = e.target.closest('.journal-delete-btn');
  if (btn) deleteJournal(Number(btn.dataset.journalId));
});

// Tree — event delegation for dynamically rendered folder/note items
document.getElementById('tree').addEventListener('click', function(e) {
  const el = e.target.closest('[data-action]');
  if (!el) return;
  if (el.dataset.action === 'open-note') openNote(Number(el.dataset.noteId));
  if (el.dataset.action === 'select-folder') selectFolder(Number(el.dataset.folderId));
});

// Token list — event delegation for dynamically rendered revoke buttons
document.getElementById('token-list').addEventListener('click', function(e) {
  const btn = e.target.closest('[data-action="revoke-token"]');
  if (btn) revokeToken(Number(btn.dataset.tokenId));
});

// Disk full banner
document.getElementById('disk-full-dismiss').addEventListener('click', function() { document.getElementById('disk-full-banner').style.display = 'none'; });
