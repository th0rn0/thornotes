/* thornotes — main app */
'use strict';

// ── State ──────────────────────────────────────────────────────────────────
let csrfToken = '';
let currentUser = null;
let currentNote = null;      // { id, title, content_hash, disk_path, folder_id, tags }
let editor = null;           // EasyMDE instance
let saveTimer = null;
let loadedFolderIds = new Set(); // tracks which folders have had their notes loaded
let folders = [];            // flat folder list from API
let notesByFolder = {};      // { folderId: [noteListItem] }
let rootNotes = [];          // notes with no folder
let searchResults = null;    // null = not in search mode

const AUTO_SAVE_DELAY_MS = 1500;

// ── Init ───────────────────────────────────────────────────────────────────
(async function init() {
  try {
    const me = await api('GET', '/api/v1/auth/me');
    currentUser = me;
    document.getElementById('topbar-username').textContent = me.username;
    const csrf = await api('GET', '/api/v1/csrf');
    csrfToken = csrf.csrf_token;
    await loadFolderTree();
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
    await loadFolderTree();
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

    html += `<div class="tree-folder" data-folder-id="${f.id}">`;
    html += `<div class="tree-folder-label" style="padding-left:${8 + indent}px" onclick="toggleFolder(${f.id})">`;
    html += `<span class="icon">${icon}</span>${esc(f.name)}`;
    html += `</div>`;

    if (expanded) {
      html += `<div class="tree-notes">`;
      for (const n of notes) {
        const active = currentNote && currentNote.id === n.id ? ' active' : '';
        html += `<div class="tree-note${active}" style="padding-left:${20 + indent}px" onclick="openNote(${n.id})" title="${esc(n.title)}">${esc(n.title)}</div>`;
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
      html += `<div class="tree-note${active}" style="padding-left:12px" onclick="openNote(${n.id})" title="${esc(n.title)}">${esc(n.title)}</div>`;
    }
  }

  tree.innerHTML = html;
}

async function toggleFolder(folderId) {
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
    html += `<div class="tree-note${active}" onclick="openNote(${r.note_id})" title="${esc(r.title)}">${esc(r.title)}</div>`;
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

async function shareNote() {
  if (!currentNote) return;
  const res = await api('POST', `/api/v1/notes/${currentNote.id}/share`, {});
  const token = res.share_token;
  const url = `${location.origin}/s/${token}`;
  await navigator.clipboard.writeText(url).catch(() => {});
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

// ── Utils ──────────────────────────────────────────────────────────────────
function esc(s) {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}
