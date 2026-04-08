/* thornotes — main app */
'use strict';

// ── Theme ──────────────────────────────────────────────────────────────────
// FOUC prevention runs in <head> before paint (see app.html).
// This block handles runtime switching and OS-preference live-updates.
const VALID_THEMES = ['auto', 'light', 'dark', 'catppuccin', 'nord', 'tokyonight', 'solarized'];
const hljsThemeEl = document.getElementById('hljs-theme');
const metaThemeColor = document.getElementById('meta-theme-color');
const hljsHref = (t) => ({
  light: '/static/css/highlight-github.min.css',
  dark: '/static/css/highlight-github-dark.min.css',
  catppuccin: '/static/css/highlight-catppuccin-mocha.min.css',
  nord: '/static/css/highlight-github-dark.min.css',
  tokyonight: '/static/css/highlight-github-dark.min.css',
  solarized: '/static/css/highlight-github.min.css',
})[t] || '/static/css/highlight-github.min.css';
const themeColors = { light: '#f5f5f5', dark: '#252526', catppuccin: '#1e1e2e', nord: '#3b4252', tokyonight: '#16161e', solarized: '#eee8d5' };
function resolveAuto() {
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}
function applyTheme(name) {
  const resolved = name === 'auto' ? resolveAuto() : name;
  document.documentElement.setAttribute('data-theme', resolved);
  if (hljsThemeEl) hljsThemeEl.href = hljsHref(resolved);
  if (metaThemeColor) metaThemeColor.content = themeColors[resolved] || '';
  if (editor) editor.setTheme(resolved);
  try { localStorage.setItem('theme', name); } catch(e) {}
}
// Live OS-preference switch (only fires when stored theme is 'auto').
window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', function() {
  let saved; try { saved = localStorage.getItem('theme'); } catch(e) {}
  if (!saved || saved === 'auto') applyTheme('auto');
});

// ── State ──────────────────────────────────────────────────────────────────
let csrfToken = '';
let currentUser = null;
let currentNote = null;      // { id, title, content_hash, disk_path, folder_id, tags }
let currentFolderId = null;  // highlighted folder in the tree
let editor = null;           // CM6 editor instance
let editorPreviewEl = null;  // CM6 preview pane element
let editorPreviewOpen = false;
let editorSplitOpen = false;
let editorPreviewEditOpen = false;
let _previewEditBlocks = []; // [{raw, type}] from marked.lexer — used in preview-edit mode
let saveTimer = null;
let _loadingNote = false;  // suppresses auto-save during editor.setValue()
let loadedFolderIds = new Set(); // tracks which folders have had their notes loaded
let folders = [];            // flat folder list from API
let notesByFolder = {};      // { folderId: [noteListItem] }
let rootNotes = [];          // notes with no folder
let searchResults = null;    // null = not in search mode
let _pendingTablePaste = null; // { from, text, rows } set while paste-convert bar is visible
let journals = [];           // all journals for current user
let currentFolderViewId = null; // folder ID currently shown in folder view (null = not shown)

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
    await resolveDeepLink(window.location.pathname).catch(() => {});
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
    await resolveDeepLink(window.location.pathname).catch(() => {});
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
  let saved; try { saved = localStorage.getItem('theme'); } catch(e) {}
  const sel = document.getElementById('theme-select');
  if (sel) sel.value = VALID_THEMES.indexOf(saved) !== -1 ? saved : 'auto';
  applyTheme(sel ? sel.value : 'auto');
  // Load auto-collapse setting
  const acToggle = document.getElementById('auto-collapse-toggle');
  if (acToggle) acToggle.checked = localStorage.getItem('autoCollapse') !== 'false';
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
            if (editor) { _loadingNote = true; editor.setValue(fresh.content); _loadingNote = false; }
            document.getElementById('note-title').value = fresh.title;
            document.getElementById('note-tags').value = (fresh.tags || []).join(', ');
            document.getElementById('note-stats').textContent = noteStats(fresh.content);
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
    html += `<div class="tree-folder-label${folderActive}" style="padding-left:${8 + indent}px" data-action="select-folder" data-folder-id="${f.id}" draggable="true">`;
    html += `<span class="icon">${icon}</span>${esc(f.name)}`;
    html += `</div>`;

    if (expanded) {
      html += `<div class="tree-notes">`;
      for (const n of notes) {
        const active = currentNote && currentNote.id === n.id ? ' active' : '';
        html += `<div class="tree-note${active}" style="padding-left:${20 + indent}px" data-action="open-note" data-note-id="${n.id}" title="${esc(n.title)}" draggable="true">${esc(n.title)}</div>`;
      }
      for (const child of children) {
        renderFolder(child, depth + 1);
      }
      html += `</div>`;
    }

    html += `</div>`;
  }

  for (const f of roots) renderFolder(f);

  // Root notes.
  if (rootNotes.length > 0) {
    html += `<div class="tree-unsorted drop-root">Root</div>`;
    for (const n of rootNotes) {
      const active = currentNote && currentNote.id === n.id ? ' active' : '';
      html += `<div class="tree-note${active}" style="padding-left:12px" data-action="open-note" data-note-id="${n.id}" title="${esc(n.title)}" draggable="true">${esc(n.title)}</div>`;
    }
  } else {
    // No root notes yet — invisible until a drag starts.
    html += `<div class="tree-root-drop drop-root">Root</div>`;
  }

  tree.innerHTML = html;
}

async function selectFolder(folderId) {
  currentNote = null;
  currentFolderId = folderId;
  if (loadedFolderIds.has(folderId)) {
    loadedFolderIds.delete(folderId);
    renderTree();
  } else {
    await loadFolderNotes(folderId);
  }
  showFolderView(folderId);
}

// ── Drag-and-drop tree ─────────────────────────────────────────────────────
let _dndPayload = null; // { type: 'note'|'folder', id: number }

(function initTreeDnd() {
  const treeEl = document.getElementById('tree');

  treeEl.addEventListener('dragstart', function(e) {
    const noteEl = e.target.closest('[data-note-id]');
    const folderEl = e.target.closest('[data-folder-id][draggable]');
    if (noteEl) {
      _dndPayload = { type: 'note', id: parseInt(noteEl.dataset.noteId) };
    } else if (folderEl) {
      _dndPayload = { type: 'folder', id: parseInt(folderEl.dataset.folderId) };
    } else {
      e.preventDefault();
      return;
    }
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', ''); // required for Firefox
    e.target.classList.add('dnd-dragging');
    treeEl.classList.add('dnd-active');
  });

  treeEl.addEventListener('dragend', function(e) {
    _dndPayload = null;
    treeEl.classList.remove('dnd-active');
    treeEl.querySelectorAll('.dnd-dragging').forEach(el => el.classList.remove('dnd-dragging'));
    treeEl.querySelectorAll('.dnd-over').forEach(el => el.classList.remove('dnd-over'));
  });

  treeEl.addEventListener('dragover', function(e) {
    if (!_dndPayload) return;
    const target = e.target.closest('.tree-folder-label, .drop-root');
    if (!target) return;
    // Can't drop a folder onto its own label.
    if (_dndPayload.type === 'folder' && target.dataset.folderId === String(_dndPayload.id)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    treeEl.querySelectorAll('.dnd-over').forEach(el => el.classList.remove('dnd-over'));
    target.classList.add('dnd-over');
  });

  treeEl.addEventListener('drop', async function(e) {
    e.preventDefault();
    treeEl.querySelectorAll('.dnd-over').forEach(el => el.classList.remove('dnd-over'));
    if (!_dndPayload) return;

    const folderLabel = e.target.closest('.tree-folder-label');
    const rootTarget = e.target.closest('.drop-root');
    if (!folderLabel && !rootTarget) return;

    // Determine destination: folder ID or null (root).
    let destFolderId = null;
    if (folderLabel) {
      const id = parseInt(folderLabel.dataset.folderId);
      if (_dndPayload.type === 'folder' && id === _dndPayload.id) return;
      destFolderId = id;
    }

    const payload = _dndPayload;
    _dndPayload = null;

    try {
      if (payload.type === 'note') {
        await api('PATCH', `/api/v1/notes/${payload.id}/move`, { folder_id: destFolderId });
      } else {
        await api('PATCH', `/api/v1/folders/${payload.id}/move`, { parent_id: destFolderId });
      }
    } catch (err) {
      showNotification(err.message || 'Move failed', true);
      return;
    }

    // Reload tree, preserving which folders were expanded.
    const wasExpanded = new Set(loadedFolderIds);
    notesByFolder = {};
    loadedFolderIds = new Set();
    [folders, rootNotes] = await Promise.all([
      api('GET', '/api/v1/folders'),
      api('GET', '/api/v1/notes/root'),
    ]);
    await Promise.all([...wasExpanded].map(async id => {
      if (folders.find(f => f.id === id)) {
        const items = await api('GET', `/api/v1/folders/${id}/notes`).catch(() => []);
        notesByFolder[id] = items || [];
        loadedFolderIds.add(id);
      }
    }));
    renderTree();
  });
})();

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

// ── Deep linking ───────────────────────────────────────────────────────────
function folderAncestorPath(folderId) {
  const segments = [];
  let id = folderId;
  while (id != null) {
    const f = folders.find(f => f.id === id);
    if (!f) break;
    segments.unshift(encodeURIComponent(f.name));
    id = f.parent_id;
  }
  return segments.join('/');
}

function noteDeepLink(note) {
  if (!note.slug) return '/?note=' + note.id;
  const parts = [];
  if (note.folder_id != null) {
    const fp = folderAncestorPath(note.folder_id);
    if (fp) parts.push(fp);
  }
  parts.push(encodeURIComponent(note.slug));
  return '/' + parts.join('/');
}

async function resolveDeepLink(pathname) {
  if (!pathname || pathname === '/') return;
  const segments = pathname.split('/').filter(Boolean).map(s => decodeURIComponent(s));
  if (segments.length === 0) return;

  const noteSlug = segments[segments.length - 1];
  const folderNames = segments.slice(0, -1);

  // Walk folder tree to find target folder.
  let targetFolderId = null;
  if (folderNames.length > 0) {
    let current = folders.find(f => !f.parent_id && f.name === folderNames[0]);
    if (!current) return;
    for (let i = 1; i < folderNames.length; i++) {
      current = folders.find(f => f.parent_id === current.id && f.name === folderNames[i]);
      if (!current) return;
    }
    targetFolderId = current.id;
  }

  // Ensure notes are loaded for that folder, then find by slug.
  if (targetFolderId != null) {
    await ensureFolderLoaded(targetFolderId);
    const match = (notesByFolder[targetFolderId] || []).find(n => n.slug === noteSlug);
    if (match) await openNote(match.id, { historyMode: 'replace' });
  } else {
    const match = rootNotes.find(n => n.slug === noteSlug);
    if (match) await openNote(match.id, { historyMode: 'replace' });
  }
}

// ── Note ops ───────────────────────────────────────────────────────────────
async function openNote(noteId, { historyMode = 'push' } = {}) {
  const note = await api('GET', `/api/v1/notes/${noteId}`);
  currentNote = note;
  if (isMobile()) closeSidebar();

  currentFolderId = null;
  document.getElementById('empty-state').style.display = 'none';
  document.getElementById('folder-view').style.display = 'none';
  currentFolderViewId = null;
  const container = document.getElementById('editor-container');
  container.style.display = 'flex';

  document.getElementById('note-title').value = note.title;
  document.getElementById('note-tags').value = (note.tags || []).join(', ');
  document.getElementById('note-path').textContent = note.disk_path;
  document.getElementById('note-stats').textContent = noteStats(note.content);
  setSaveStatus('saved');

  if (!editor) {
    const editorArea = document.getElementById('editor-area');
    editorArea.innerHTML = '';

    // Toolbar
    const toolbar = document.createElement('div');
    toolbar.className = 'cm6-toolbar';
    toolbar.innerHTML =
      '<button data-cmd="bold" title="Bold"><b>B</b></button>' +
      '<button data-cmd="italic" title="Italic"><i>I</i></button>' +
      '<button data-cmd="heading" title="Heading">H#</button>' +
      '<span class="cm6-sep"></span>' +
      '<button data-cmd="quote" title="Blockquote">Quote</button>' +
      '<button data-cmd="unorderedList" title="Bullet list">• List</button>' +
      '<button data-cmd="orderedList" title="Numbered list">1. List</button>' +
      '<span class="cm6-sep"></span>' +
      '<button data-cmd="link" title="Insert link">Link</button>' +
      '<button data-cmd="table" title="Insert table" id="cm6-table-btn">Table</button>' +
      '<button data-cmd="formatTable" title="Format table columns">Fmt</button>' +
      '<span class="cm6-sep"></span>' +
      '<button data-cmd="preview" title="Toggle preview" id="cm6-preview-btn">Preview</button>' +
      '<button data-cmd="previewedit" title="Preview Edit — click any block to edit inline" id="cm6-previewedit-btn">P.Edit</button>' +
      '<button data-cmd="split" title="Split editor / preview" id="cm6-split-btn">Split</button>' +
      '<button data-cmd="lineNumbers" title="Toggle line numbers" id="cm6-linenumbers-btn">Ln#</button>' +
      '<span class="cm6-sep"></span>' +
      '<button data-cmd="undo" title="Undo">Undo</button>' +
      '<button data-cmd="redo" title="Redo">Redo</button>';
    editorArea.appendChild(toolbar);

    // Table paste conversion bar (hidden until tabular content is detected)
    const pasteBar = document.createElement('div');
    pasteBar.id = 'table-paste-bar';
    pasteBar.className = 'table-paste-bar';
    pasteBar.setAttribute('role', 'status');
    pasteBar.setAttribute('aria-live', 'polite');
    pasteBar.innerHTML =
      '<span class="tpb-info"></span>' +
      '<button class="tpb-convert" id="table-paste-convert-btn">Convert to Markdown table</button>' +
      '<button class="tpb-dismiss" id="table-paste-dismiss-btn" aria-label="Dismiss">\u00d7</button>';
    editorArea.appendChild(pasteBar);

    // Lint panel (hidden until toggled)
    const lintPanel = document.createElement('div');
    lintPanel.id = 'lint-panel';
    lintPanel.className = 'lint-panel';
    editorArea.appendChild(lintPanel);

    // Editor + preview wrapper
    const wrap = document.createElement('div');
    wrap.className = 'cm6-wrap';
    editorArea.appendChild(wrap);

    // CM6 mount point
    const mount = document.createElement('div');
    mount.className = 'cm6-mount';
    wrap.appendChild(mount);

    // Preview pane
    editorPreviewEl = document.createElement('div');
    editorPreviewEl.className = 'cm6-preview markdown-body';
    editorPreviewEl.style.display = 'none';
    wrap.appendChild(editorPreviewEl);

    // Intercept wikilink clicks + preview-edit block clicks in the preview pane.
    editorPreviewEl.addEventListener('click', function(e) {
      const a = e.target.closest('a.wikilink[data-note-id]');
      if (a) {
        e.preventDefault();
        const id = parseInt(a.dataset.noteId);
        if (id) openNote(id);
        return;
      }
      const cb = e.target.closest('input[type="checkbox"]');
      if (cb) {
        e.preventDefault();
        const allCbs = Array.from(editorPreviewEl.querySelectorAll('input[type="checkbox"]'));
        const idx = allCbs.indexOf(cb);
        if (idx === -1) return;
        const md = editor.getValue();
        let count = 0;
        const newMd = md.replace(/^([ \t]*[-*+] )(\[[ xX]\])/gm, function(match, prefix, box) {
          if (count++ === idx) return prefix + (/[xX]/.test(box) ? '[ ]' : '[x]');
          return match;
        });
        if (newMd !== md) editor.setValue(newMd);
        return;
      }
      if (!editorPreviewEditOpen) return;
      const block = e.target.closest('.pe-block[data-pe-idx]');
      if (!block) return;
      openPreviewEditInlineEditor(block, +block.dataset.peIdx);
    });

    editor = CM6.createEditor(mount, {
      onChange: onEditorChange,
      theme: document.documentElement.getAttribute('data-theme') || 'light',
    });

    toolbar.addEventListener('click', function(e) {
      const btn = e.target.closest('[data-cmd]');
      if (!btn) return;
      const cmd = btn.dataset.cmd;
      if (cmd === 'preview') {
        toggleEditorPreview();
      } else if (cmd === 'previewedit') {
        toggleEditorPreviewEdit();
      } else if (cmd === 'split') {
        toggleEditorSplit();
      } else if (cmd === 'lineNumbers') {
        toggleLineNumbers();
      } else if (cmd === 'table') {
        handleTableBtn(e, btn);
      } else if (cmd === 'formatTable') {
        formatTableAtCursor();
      } else if (CM6.commands[cmd]) {
        CM6.commands[cmd](editor);
      }
    });

    // Paste bar actions
    document.getElementById('table-paste-convert-btn').addEventListener('click', convertPasteToTable);
    document.getElementById('table-paste-dismiss-btn').addEventListener('click', hideTablePasteBar);

    // Paste detection — fires before CM6 processes the paste so we can measure
    // where the insertion will land, then show the bar after CM6 is done.
    mount.addEventListener('paste', function(e) {
      if (!editor) return;
      const text = (e.clipboardData || window.clipboardData || {}).getData('text/plain') || '';
      const rows = parseTabularText(text);
      if (!rows) return;
      const from = editor._view.state.selection.main.from;
      // CM6 normalises \r\n → \n; pre-normalise so we can compute the end offset.
      const normalised = text.replace(/\r\n/g, '\n').replace(/\r/g, '\n');
      _pendingTablePaste = { from, text: normalised, rows };
      requestAnimationFrame(function() {
        const bar = document.getElementById('table-paste-bar');
        if (!bar) return;
        bar.querySelector('.tpb-info').textContent =
          'Pasted ' + rows.length + ' row' + (rows.length !== 1 ? 's' : '') +
          ' \u00d7 ' + rows[0].length + ' columns \u2014 convert to Markdown table?';
        bar.style.display = 'flex';
      });
    });

    // Editor context menu (right-click).
    mount.addEventListener('contextmenu', function(e) {
      if (!editor) return;
      e.preventDefault();
      const menu = document.getElementById('editor-ctx-menu');
      const sel = editor._view.state.selection.main;
      const hasSelection = !sel.empty;
      // Show formatting actions only when text is selected; always show table actions
      menu.querySelectorAll('[data-editor-ctx-action="bold"],[data-editor-ctx-action="italic"],[data-editor-ctx-action="quote"],[data-editor-ctx-action="unorderedList"],[data-editor-ctx-action="link"],[data-editor-ctx-action="to-table"]')
        .forEach(btn => { btn.style.display = hasSelection ? '' : 'none'; });
      const mw = 180, mh = menu.querySelectorAll('button:not([style*="none"])').length * 36 + 16;
      menu.style.left = Math.min(e.clientX, window.innerWidth - mw) + 'px';
      menu.style.top  = Math.min(e.clientY, window.innerHeight - mh) + 'px';
      menu.style.display = 'block';
    });

    // Restore line numbers preference on first editor init.
    applyLineNumbers();
    // Restore split/preview mode preference.
    const savedMode = localStorage.getItem('editorViewMode');
    if (savedMode === 'split') toggleEditorSplit();
    else if (savedMode === 'preview') toggleEditorPreview();
    else if (savedMode === 'previewedit') toggleEditorPreviewEdit();
  }

  _loadingNote = true;
  editor.setValue(note.content);
  _loadingNote = false;

  // Update URL to reflect the open note.
  const deepLink = noteDeepLink(note);
  if (historyMode === 'push') {
    history.pushState({ noteId: note.id }, '', deepLink);
  } else if (historyMode === 'replace') {
    history.replaceState({ noteId: note.id }, '', deepLink);
  }
  document.title = 'thornotes \u2014 ' + note.title;

  renderTree(); // refresh active state
}

// ── Wiki-links ─────────────────────────────────────────────────────────────
// Build a map of lowercased note title → note ID from all loaded notes.
function buildNoteTitleMap() {
  const map = {};
  for (const n of rootNotes) map[n.title.toLowerCase()] = n.id;
  for (const fid of Object.keys(notesByFolder)) {
    for (const n of notesByFolder[fid]) map[n.title.toLowerCase()] = n.id;
  }
  return map;
}

// Replace [[Note Title]] wikilinks with markdown links before parsing.
// Known titles resolve to #tn-<id> fragments (intercepted by click handler).
// Unknown titles render as a styled dead link.
function processWikilinks(content) {
  const titleMap = buildNoteTitleMap();
  return content.replace(/\[\[([^\]\n]+)\]\]/g, function(_, title) {
    const id = titleMap[title.toLowerCase()];
    if (id != null) return `[${title}](#tn-${id})`;
    return `[${title}](#tn-)`;
  });
}

function _applyPreviewPostProcess(tmp) {
  tmp.querySelectorAll('a[href^="#tn-"]').forEach(a => {
    const id = a.getAttribute('href').slice(4);
    if (id) {
      a.className = 'wikilink';
      a.dataset.noteId = id;
    } else {
      a.className = 'wikilink wikilink-missing';
    }
  });
  tmp.querySelectorAll('pre code').forEach(el => hljs.highlightElement(el));
  tmp.querySelectorAll('input[type="checkbox"]').forEach(cb => cb.removeAttribute('disabled'));
}

function renderPreviewContent(content) {
  if (editorPreviewEditOpen) {
    renderPreviewEditContent(content);
    return;
  }
  const processed = processWikilinks(content);
  const html = marked.parse(processed);
  const tmp = document.createElement('div');
  tmp.innerHTML = html;
  _applyPreviewPostProcess(tmp);
  editorPreviewEl.innerHTML = tmp.innerHTML;
}

// Render content in preview-edit mode: wrap each block in a .pe-block div
// annotated with its token index so clicks can round-trip back to markdown source.
function renderPreviewEditContent(rawContent) {
  const tokens = marked.lexer(rawContent);
  _previewEditBlocks = tokens.map(tok => ({ raw: tok.raw, type: tok.type }));

  // Render the full document (wikilinks + hljs) into a temp container.
  const processed = processWikilinks(rawContent);
  const tmp = document.createElement('div');
  tmp.innerHTML = marked.parse(processed);
  _applyPreviewPostProcess(tmp);

  // Build an index: non-space token positions in _previewEditBlocks.
  const nonSpaceIdxs = [];
  _previewEditBlocks.forEach(function(b, i) { if (b.type !== 'space') nonSpaceIdxs.push(i); });

  // Wrap each rendered top-level child in a pe-block div keyed to its token index.
  editorPreviewEl.innerHTML = '';
  Array.from(tmp.children).forEach(function(el, i) {
    const wrap = document.createElement('div');
    wrap.className = 'pe-block';
    if (nonSpaceIdxs[i] !== undefined) wrap.dataset.peIdx = nonSpaceIdxs[i];
    wrap.appendChild(el);
    editorPreviewEl.appendChild(wrap);
  });

  // Hint at the bottom.
  const hint = document.createElement('p');
  hint.className = 'cm6-preview-edit-hint';
  hint.textContent = 'Click any block to edit \u2022 Ctrl+Enter to save \u2022 Esc to cancel';
  editorPreviewEl.appendChild(hint);
}

// Open an inline textarea editor over a .pe-block element.
function openPreviewEditInlineEditor(block, idx) {
  if (block.querySelector('.pe-inline-editor')) return; // already editing
  const blockData = _previewEditBlocks[idx];
  if (!blockData) return;

  const savedChildren = Array.from(block.childNodes).map(n => n.cloneNode(true));
  block.innerHTML = '';

  // Raw markdown textarea (compact, at top).
  const ta = document.createElement('textarea');
  ta.className = 'pe-inline-editor';
  const rawTrimmed = blockData.raw.trimEnd();
  const rawTrail = blockData.raw.slice(rawTrimmed.length) || '\n';
  ta.value = rawTrimmed;
  block.appendChild(ta);

  // Live rendered preview — updates on every keystroke.
  const liveEl = document.createElement('div');
  liveEl.className = 'pe-live-preview markdown-body';
  block.appendChild(liveEl);

  function updateLive() {
    const raw = ta.value;
    const processed = processWikilinks(raw);
    const tmp = document.createElement('div');
    tmp.innerHTML = marked.parse(processed);
    _applyPreviewPostProcess(tmp);
    liveEl.innerHTML = tmp.innerHTML;
  }
  updateLive();

  requestAnimationFrame(function() {
    ta.style.height = ta.scrollHeight + 'px';
    ta.focus();
    ta.setSelectionRange(0, ta.value.length);
  });

  ta.addEventListener('input', function() {
    ta.style.height = 'auto';
    ta.style.height = ta.scrollHeight + 'px';
    updateLive();
  });

  let done = false;

  function commitEdit() {
    if (done) return;
    done = true;
    _previewEditBlocks[idx].raw = ta.value.trimEnd() + rawTrail;
    const newContent = _previewEditBlocks.map(function(b) { return b.raw; }).join('');
    _loadingNote = true;
    editor.setValue(newContent);
    _loadingNote = false;
    document.getElementById('note-stats').textContent = noteStats(newContent);
    setSaveStatus('saving');
    clearTimeout(saveTimer);
    saveTimer = setTimeout(autoSave, AUTO_SAVE_DELAY_MS);
    renderPreviewContent(newContent);
  }

  function cancelEdit() {
    if (done) return;
    done = true;
    block.innerHTML = '';
    savedChildren.forEach(function(n) { block.appendChild(n); });
  }

  ta.addEventListener('blur', commitEdit);

  ta.addEventListener('keydown', function(e) {
    if (e.key === 'Escape') {
      e.preventDefault();
      ta.removeEventListener('blur', commitEdit);
      cancelEdit();
    } else if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      ta.removeEventListener('blur', commitEdit);
      commitEdit();
    }
  });
}

// ── Folder view ────────────────────────────────────────────────────────────
function showFolderView(folderId) {
  const folder = folders.find(f => f.id === folderId);
  const notes = notesByFolder[folderId] || [];
  const folderView = document.getElementById('folder-view');
  const emptyState = document.getElementById('empty-state');
  const editorContainer = document.getElementById('editor-container');

  currentFolderViewId = folderId;
  emptyState.style.display = 'none';
  editorContainer.style.display = 'none';
  folderView.style.display = 'flex';

  const titleEl = document.getElementById('folder-view-title');
  titleEl.textContent = folder ? folder.name : 'Folder';

  const grid = document.getElementById('folder-view-grid');
  if (notes.length === 0) {
    grid.innerHTML = '<div class="folder-view-empty">No notes in this folder yet.</div>';
    return;
  }

  grid.innerHTML = notes.map(n => {
    const tags = (n.tags || []).map(t => `<span class="fv-tag">${esc(t)}</span>`).join('');
    return `<div class="fv-card" data-action="open-note" data-note-id="${n.id}">
      <div class="fv-card-title">${esc(n.title)}</div>
      ${tags ? `<div class="fv-tags">${tags}</div>` : ''}
      <div class="fv-snippet" data-note-id="${n.id}"></div>
    </div>`;
  }).join('');

  // Lazily load content snippets for each note (up to 20).
  const toLoad = notes.slice(0, 20);
  toLoad.forEach(n => {
    api('GET', `/api/v1/notes/${n.id}`).then(note => {
      if (currentFolderViewId !== folderId) return; // stale
      const snipEl = grid.querySelector(`.fv-snippet[data-note-id="${note.id}"]`);
      if (snipEl && note.content) {
        const plain = note.content.replace(/^#+\s+.*$/mg, '').replace(/[*_`~[\]]/g, '').trim();
        snipEl.textContent = plain.slice(0, 200) + (plain.length > 200 ? '…' : '');
      }
    }).catch(() => {});
  });
}

function closeFolderView() {
  currentFolderViewId = null;
  document.getElementById('folder-view').style.display = 'none';
}

function noteStats(content) {
  const chars = content.length;
  const lines = content === '' ? 0 : content.split('\n').length;
  return `${chars} chars · ${lines} line${lines !== 1 ? 's' : ''}`;
}

function applyLineNumbers() {
  const on = localStorage.getItem('lineNumbers') === 'true';
  const mount = document.querySelector('.cm6-mount');
  const btn = document.getElementById('cm6-linenumbers-btn');
  if (mount) mount.classList.toggle('show-line-numbers', on);
  if (btn) btn.classList.toggle('active', on);
}

function toggleLineNumbers() {
  const on = localStorage.getItem('lineNumbers') !== 'true';
  localStorage.setItem('lineNumbers', on);
  applyLineNumbers();
}

// ── Markdown linter ──────────────────────────────────────────────────────────

function lintMarkdown(md) {
  const issues = [];
  const lines = md.split('\n');
  let lastHeadingLevel = 0;
  let blankCount = 0;
  let inFence = false;

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const lineNum = i + 1;

    // Track fenced code blocks — don't lint inside them
    if (/^```/.test(line)) { inFence = !inFence; blankCount = 0; continue; }
    if (inFence) { blankCount = 0; continue; }

    const blank = line.trim() === '';
    if (blank) { blankCount++; } else { blankCount = 0; }

    // Trailing whitespace
    if (!blank && / +$/.test(line)) {
      issues.push({ line: lineNum, severity: 'warn', msg: 'Trailing whitespace' });
    }

    // Hard tabs
    if (/\t/.test(line)) {
      issues.push({ line: lineNum, severity: 'warn', msg: 'Hard tab character' });
    }

    // Multiple consecutive blank lines
    if (blankCount === 3) {
      issues.push({ line: lineNum, severity: 'warn', msg: 'Multiple consecutive blank lines' });
    }

    // Heading checks
    const headingMatch = line.match(/^(#{1,6})\s/);
    if (headingMatch) {
      const level = headingMatch[1].length;

      // Heading level jumps (e.g. # then ###)
      if (lastHeadingLevel > 0 && level > lastHeadingLevel + 1) {
        issues.push({ line: lineNum, severity: 'warn', msg: `Heading jumps from h${lastHeadingLevel} to h${level}` });
      }
      lastHeadingLevel = level;

      // No blank line before heading (except first line or after another heading)
      if (i > 0 && lines[i - 1].trim() !== '' && !/^#{1,6}\s/.test(lines[i - 1])) {
        issues.push({ line: lineNum, severity: 'warn', msg: 'No blank line before heading' });
      }
    }

    // Bare URLs (http/https not inside [] or <>)
    const bareUrlRe = /(?<![(<\[])(https?:\/\/[^\s)>\]]+)/g;
    let m;
    while ((m = bareUrlRe.exec(line)) !== null) {
      issues.push({ line: lineNum, severity: 'warn', msg: `Bare URL: ${m[1].slice(0, 50)}${m[1].length > 50 ? '…' : ''}` });
    }
  }

  return issues;
}

function applyLintSetting() {
  const enabled = localStorage.getItem('autoLint') === 'true';
  const panel = document.getElementById('lint-panel');
  if (!panel) return;
  if (enabled) {
    panel.classList.add('open');
    runLint();
  } else {
    panel.classList.remove('open');
  }
}

function runLint() {
  if (localStorage.getItem('autoLint') !== 'true' || !editor) return;
  const panel = document.getElementById('lint-panel');
  if (!panel) return;
  const issues = lintMarkdown(editor.getValue());
  panel.innerHTML = '';
  if (issues.length === 0) {
    panel.innerHTML = '<div class="lint-panel-empty">No issues found</div>';
  } else {
    issues.forEach(function(issue) {
      const row = document.createElement('div');
      row.className = `lint-issue ${issue.severity}`;
      row.innerHTML = `<span class="lint-issue-line">Line ${issue.line}</span><span class="lint-issue-msg">${issue.msg}</span>`;
      row.addEventListener('click', function() {
        if (!editor) return;
        const view = editor._view;
        const line = view.state.doc.line(Math.min(issue.line, view.state.doc.lines));
        view.dispatch({ selection: { anchor: line.from }, scrollIntoView: true });
        view.focus();
      });
      panel.appendChild(row);
    });
  }
}

function closePreviewEditMode() {
  if (!editorPreviewEditOpen) return;
  editorPreviewEditOpen = false;
  _previewEditBlocks = [];
  const wrap = document.querySelector('.cm6-wrap');
  if (wrap) wrap.classList.remove('preview-edit');
  const peb = document.getElementById('cm6-previewedit-btn');
  if (peb) peb.classList.remove('active');
}

function toggleEditorPreview() {
  editorPreviewOpen = !editorPreviewOpen;
  const mount = document.querySelector('.cm6-mount');
  const wrap  = document.querySelector('.cm6-wrap');
  const btn   = document.getElementById('cm6-preview-btn');
  if (editorPreviewOpen) {
    // Close split and preview-edit if open.
    if (editorSplitOpen) {
      editorSplitOpen = false;
      wrap.classList.remove('split');
      const sb = document.getElementById('cm6-split-btn');
      if (sb) sb.classList.remove('active');
    }
    closePreviewEditMode();
    renderPreviewContent(editor.getValue());
    editorPreviewEl.style.display = '';
    mount.style.display = 'none';
    btn.classList.add('active');
    localStorage.setItem('editorViewMode', 'preview');
  } else {
    editorPreviewEl.style.display = 'none';
    mount.style.display = '';
    btn.classList.remove('active');
    localStorage.setItem('editorViewMode', 'editor');
    editor.focus();
  }
}

function toggleEditorSplit() {
  editorSplitOpen = !editorSplitOpen;
  const mount = document.querySelector('.cm6-mount');
  const wrap  = document.querySelector('.cm6-wrap');
  const btn   = document.getElementById('cm6-split-btn');
  if (editorSplitOpen) {
    // Close preview-only and preview-edit if open.
    if (editorPreviewOpen) {
      editorPreviewOpen = false;
      const pb = document.getElementById('cm6-preview-btn');
      if (pb) pb.classList.remove('active');
    }
    closePreviewEditMode();
    renderPreviewContent(editor.getValue());
    mount.style.display = '';
    editorPreviewEl.style.display = '';
    wrap.classList.add('split');
    btn.classList.add('active');
    localStorage.setItem('editorViewMode', 'split');
  } else {
    editorPreviewEl.style.display = 'none';
    wrap.classList.remove('split');
    btn.classList.remove('active');
    localStorage.setItem('editorViewMode', 'editor');
    editor.focus();
  }
}

function toggleEditorPreviewEdit() {
  editorPreviewEditOpen = !editorPreviewEditOpen;
  const mount = document.querySelector('.cm6-mount');
  const wrap  = document.querySelector('.cm6-wrap');
  const btn   = document.getElementById('cm6-previewedit-btn');
  if (editorPreviewEditOpen) {
    // Close split and preview if open.
    if (editorSplitOpen) {
      editorSplitOpen = false;
      wrap.classList.remove('split');
      const sb = document.getElementById('cm6-split-btn');
      if (sb) sb.classList.remove('active');
    }
    if (editorPreviewOpen) {
      editorPreviewOpen = false;
      const pb = document.getElementById('cm6-preview-btn');
      if (pb) pb.classList.remove('active');
    }
    wrap.classList.add('preview-edit');
    renderPreviewContent(editor.getValue());
    editorPreviewEl.style.display = '';
    mount.style.display = 'none';
    btn.classList.add('active');
    localStorage.setItem('editorViewMode', 'previewedit');
  } else {
    _previewEditBlocks = [];
    wrap.classList.remove('preview-edit');
    editorPreviewEl.style.display = 'none';
    mount.style.display = '';
    btn.classList.remove('active');
    localStorage.setItem('editorViewMode', 'editor');
    editor.focus();
  }
}

// ── Markdown table formatter ──────────────────────────────────────────────

function _tableParseRow(text) {
  const inner = text.trim().replace(/^\|/, '').replace(/\|$/, '');
  return inner.split('|').map(c => c.trim());
}
function _tableSepRow(cells) {
  return cells.length > 0 && cells.every(c => /^[-: ]+$/.test(c) || c === '');
}
function _tableFormat(lines) {
  const rows = lines.map(_tableParseRow);
  const maxCols = Math.max(...rows.map(r => r.length));
  rows.forEach(r => { while (r.length < maxCols) r.push(''); });
  const widths = Array.from({ length: maxCols }, (_, c) =>
    Math.max(3, ...rows.map(r => (r[c] || '').length))
  );
  const formatted = rows.map(row => {
    const sep = _tableSepRow(row);
    return '| ' + row.map((c, i) => sep ? '-'.repeat(widths[i]) : c.padEnd(widths[i])).join(' | ') + ' |';
  });
  return { formatted, widths };
}

function formatTableAtCursor() {
  const view = editor._view;
  const state = view.state;
  const pos = state.selection.main.head;
  const line = state.doc.lineAt(pos);

  if (!line.text.trim().startsWith('|')) {
    showNotification('Cursor is not inside a markdown table');
    return;
  }

  let startLn = line.number, endLn = line.number;
  while (startLn > 1 && state.doc.line(startLn - 1).text.trim().startsWith('|')) startLn--;
  while (endLn < state.doc.lines && state.doc.line(endLn + 1).text.trim().startsWith('|')) endLn++;

  const tableLines = [];
  for (let i = startLn; i <= endLn; i++) tableLines.push(state.doc.line(i).text);

  const { formatted } = _tableFormat(tableLines);
  const tableFrom = state.doc.line(startLn).from;
  const tableTo = state.doc.line(endLn).to;
  const newText = formatted.join('\n');

  if (newText === tableLines.join('\n')) return; // already formatted

  // Keep cursor on same row, col 0
  const curRow = line.number - startLn;
  const rowOffset = formatted.slice(0, curRow).join('\n').length + (curRow > 0 ? 1 : 0);

  view.dispatch({
    changes: { from: tableFrom, to: tableTo, insert: newText },
    selection: { anchor: tableFrom + rowOffset + Math.min(pos - line.from, formatted[curRow].length) },
  });
  view.focus();
}

function onEditorChange() {
  if (_loadingNote) return;
  // Dismiss the paste-conversion bar on any user edit (the paste state is
  // cleared before dispatch in convertPasteToTable so this won't close it mid-convert).
  if (_pendingTablePaste) hideTablePasteBar();
  setSaveStatus('saving');
  clearTimeout(saveTimer);
  saveTimer = setTimeout(autoSave, AUTO_SAVE_DELAY_MS);
  const content = editor.getValue();
  document.getElementById('note-stats').textContent = noteStats(content);
  if ((editorPreviewOpen || editorSplitOpen || editorPreviewEditOpen) && editorPreviewEl) {
    renderPreviewContent(content);
  }
  runLint();
}

async function autoSave() {
  if (!currentNote) return;
  const content = editor.getValue();
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
  const title = document.getElementById('note-title').value.trim();
  if (!title || title === currentNote.title) return;
  const updated = await api('PATCH', `/api/v1/notes/${currentNote.id}`, { title }).catch(() => null);
  currentNote.title = title;
  if (updated && updated.slug) {
    currentNote.slug = updated.slug;
    history.replaceState({ noteId: currentNote.id }, '', noteDeepLink(currentNote));
  }
  // Update the in-memory list so renderTree() shows the new title immediately.
  const updateInList = list => { const n = list.find(n => n.id === currentNote.id); if (n) n.title = title; };
  updateInList(rootNotes);
  for (const fid of Object.keys(notesByFolder)) updateInList(notesByFolder[fid]);
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
  sel.innerHTML = '<option value="">Root</option>';
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

// ── Note deletion ───────────────────────────────────────────────────────────
async function deleteNote(noteId, title) {
  if (!confirm(`Delete "${title}"?\n\nThis cannot be undone.`)) return;
  await api('DELETE', `/api/v1/notes/${noteId}`);
  if (currentNote && currentNote.id === noteId) {
    currentNote = null;
    document.getElementById('editor-container').style.display = 'none';
    document.getElementById('empty-state').style.display = '';
    history.pushState(null, '', '/');
  }
  // Remove the note from local state so the tree re-renders immediately.
  rootNotes = rootNotes.filter(n => n.id !== noteId);
  for (const fid of Object.keys(notesByFolder)) {
    notesByFolder[fid] = notesByFolder[fid].filter(n => n.id !== noteId);
  }
  await loadFolderTree();
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


// ── Account / API tokens ───────────────────────────────────────────────────
let _newTokenValue = '';

async function showAccountModal() {
  const endpoint = location.origin + '/mcp';
  document.getElementById('mcp-endpoint').textContent = endpoint;
  document.getElementById('mcp-endpoint-owui').textContent = endpoint;
  document.getElementById('mcp-snippet-claude').textContent =
    JSON.stringify({ mcpServers: { thornotes: { url: endpoint, headers: { Authorization: 'Bearer <your-token>' } } } }, null, 2);
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
    const scope = t.scope || 'readwrite';
    const scopeLabel = scope === 'read' ? 'Read only' : 'Read+Write';
    html += `<div class="token-item">
      <span class="token-name">${esc(t.name)}<span class="token-scope-badge ${esc(scope)}">${scopeLabel}</span></span>
      <span class="token-prefix" title="Token prefix">${esc(t.prefix)}…</span>
      <span class="token-date">created ${created} · used ${used}</span>
      <button class="token-revoke" data-action="revoke-token" data-token-id="${t.id}">Revoke</button>
    </div>`;
  }
  el.innerHTML = html;
}

async function createToken() {
  const name = document.getElementById('new-token-name').value.trim() || 'Default';
  const scope = document.getElementById('new-token-scope').value || 'readwrite';
  try {
    const token = await api('POST', '/api/v1/account/tokens', { name, scope });
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

// ── Version History ────────────────────────────────────────────────────────
let _historyEntries = [];
let _selectedHistorySha = null;

async function openHistoryModal() {
  if (!currentNote) return;
  _historyEntries = [];
  _selectedHistorySha = null;
  document.getElementById('history-modal-title').textContent = 'Version History';
  document.getElementById('history-list').innerHTML = '<div style="font-size:12px;color:#aaa;padding:12px;">Loading…</div>';
  document.getElementById('history-preview').innerHTML = '<div class="history-preview-empty">Select a version to preview</div>';
  document.getElementById('history-restore-btn').disabled = true;
  document.getElementById('history-modal').style.display = 'flex';

  try {
    const data = await api('GET', `/api/v1/notes/${currentNote.id}/history?limit=50`);
    _historyEntries = data || [];
    renderHistoryList();
  } catch (e) {
    if (e.status === 501) {
      document.getElementById('history-list').innerHTML = '';
      document.getElementById('history-preview').innerHTML =
        '<div class="history-not-available">Version history is not enabled on this server.<br><br>' +
        'Start thornotes with <code>--enable-git-history</code> to record changes.</div>';
    } else {
      document.getElementById('history-list').innerHTML =
        `<div style="font-size:12px;color:#c00;padding:12px;">${esc(e.message || 'Failed to load history')}</div>`;
    }
  }
}

function renderHistoryList() {
  const el = document.getElementById('history-list');
  if (_historyEntries.length === 0) {
    el.innerHTML = '<div style="font-size:12px;color:#aaa;padding:12px;">No history yet. Save the note to create a snapshot.</div>';
    return;
  }
  el.innerHTML = _historyEntries.map(entry => `
    <div class="history-entry" data-sha="${esc(entry.sha)}">
      <div class="history-entry-time">${esc(formatHistoryTime(entry.timestamp))}</div>
      <div class="history-entry-sha">${esc(entry.sha.slice(0, 8))}</div>
    </div>
  `).join('');
}

function formatHistoryTime(ts) {
  const d = new Date(ts);
  const now = new Date();
  const diffMs = now - d;
  const diffMin = Math.floor(diffMs / 60000);
  if (diffMin < 1) return 'Just now';
  if (diffMin < 60) return `${diffMin}m ago`;
  const diffH = Math.floor(diffMin / 60);
  if (diffH < 24) return `${diffH}h ago`;
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: d.getFullYear() !== now.getFullYear() ? 'numeric' : undefined });
}

async function selectHistoryEntry(sha) {
  _selectedHistorySha = sha;
  document.querySelectorAll('.history-entry').forEach(el => {
    el.classList.toggle('active', el.dataset.sha === sha);
  });
  document.getElementById('history-restore-btn').disabled = true;
  document.getElementById('history-preview').innerHTML = '<div class="history-preview-empty">Loading…</div>';

  try {
    const data = await api('GET', `/api/v1/notes/${currentNote.id}/history/${encodeURIComponent(sha)}`);
    document.getElementById('history-preview').innerHTML =
      `<pre class="history-preview-content">${esc(data.content || '')}</pre>`;
    document.getElementById('history-restore-btn').disabled = false;
  } catch (e) {
    document.getElementById('history-preview').innerHTML =
      `<div class="history-preview-empty">${esc(e.message || 'Failed to load version')}</div>`;
  }
}

async function restoreHistoryVersion() {
  if (!currentNote || !_selectedHistorySha) return;
  if (!confirm('Restore this version? Your current content will be replaced.')) return;

  try {
    const res = await api('POST', `/api/v1/notes/${currentNote.id}/history/${encodeURIComponent(_selectedHistorySha)}/restore`, {
      content_hash: currentNote.content_hash,
    });
    currentNote.content_hash = res.content_hash;
    editor.setValue(res.content);
    closeHistoryModal();
    showNotification('Note restored to selected version');
  } catch (e) {
    if (e.status === 409) {
      showNotification('Conflict: note was modified. Reload and try again.', true);
    } else {
      showNotification(e.message || 'Failed to restore version', true);
    }
  }
}

function closeHistoryModal() {
  document.getElementById('history-modal').style.display = 'none';
  _historyEntries = [];
  _selectedHistorySha = null;
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

// ── Settings modal ─────────────────────────────────────────────────────────
function openSettings() {
  const overlay = document.getElementById('settings-modal');
  overlay.style.display = 'flex';
  const sel = document.getElementById('theme-select');
  let saved; try { saved = localStorage.getItem('theme'); } catch(e) {}
  if (sel) sel.value = VALID_THEMES.indexOf(saved) !== -1 ? saved : 'auto';
  const acToggle = document.getElementById('auto-collapse-toggle');
  if (acToggle) acToggle.checked = localStorage.getItem('autoCollapse') !== 'false';
  const lintToggle = document.getElementById('auto-lint-toggle');
  if (lintToggle) lintToggle.checked = localStorage.getItem('autoLint') === 'true';
}
function closeSettings() {
  document.getElementById('settings-modal').style.display = 'none';
}

// ── Auto-collapse sidebar ──────────────────────────────────────────────────
let _autoCollapseTimer = null;
function _resetAutoCollapseTimer() {
  if (localStorage.getItem('autoCollapse') === 'false') return;
  if (_autoCollapseTimer) clearTimeout(_autoCollapseTimer);
  _autoCollapseTimer = setTimeout(function() {
    if (localStorage.getItem('autoCollapse') === 'false') return;
    if (loadedFolderIds.size > 0) {
      loadedFolderIds = new Set();
      renderTree();
    }
  }, 30000);
}
['mousemove', 'keydown', 'click', 'touchstart', 'scroll'].forEach(function(evt) {
  document.addEventListener(evt, _resetAutoCollapseTimer, { passive: true });
});
_resetAutoCollapseTimer();

// ── Event bindings (replaces inline onclick/onchange/oninput attrs) ─────────
// Auth
document.getElementById('login-btn').addEventListener('click', login);
document.getElementById('login-username').addEventListener('keydown', e => { if (e.key === 'Enter') login(); });
document.getElementById('login-password').addEventListener('keydown', e => { if (e.key === 'Enter') login(); });
document.getElementById('show-register-link').addEventListener('click', showRegister);
document.getElementById('register-btn').addEventListener('click', register);
document.getElementById('reg-username').addEventListener('keydown', e => { if (e.key === 'Enter') register(); });
document.getElementById('reg-password').addEventListener('keydown', e => { if (e.key === 'Enter') register(); });
document.getElementById('show-login-link').addEventListener('click', showLogin);

// Topbar
document.querySelector('.topbar-menu-btn').addEventListener('click', toggleSidebar);

// User menu dropdown
(function() {
  const menu = document.getElementById('user-menu');
  const trigger = document.getElementById('user-menu-trigger');
  const dropdown = document.getElementById('user-menu-dropdown');
  function closeMenu() {
    dropdown.classList.remove('open');
    trigger.setAttribute('aria-expanded', 'false');
  }
  trigger.addEventListener('click', function() {
    const isOpen = dropdown.classList.contains('open');
    dropdown.classList.toggle('open', !isOpen);
    trigger.setAttribute('aria-expanded', String(!isOpen));
  });
  // Close after any menu item is activated
  dropdown.addEventListener('click', closeMenu);
  // Close when clicking outside the whole menu
  document.addEventListener('click', function(e) {
    if (!menu.contains(e.target)) closeMenu();
  });
})();

document.getElementById('settings-btn').addEventListener('click', openSettings);
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
document.getElementById('mcp-copy-claude').addEventListener('click', function() {
  const text = document.getElementById('mcp-snippet-claude').textContent;
  copyToClipboard(text).then(() => showNotification('Config copied to clipboard')).catch(() => {});
});
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

// Delete note button in titlebar
document.getElementById('delete-note-btn').addEventListener('click', function() {
  if (currentNote) deleteNote(currentNote.id, currentNote.title);
});

// Tree — event delegation for dynamically rendered folder/note items
document.getElementById('tree').addEventListener('click', function(e) {
  const el = e.target.closest('[data-action]');
  if (!el) return;
  if (el.dataset.action === 'open-note') openNote(Number(el.dataset.noteId));
  if (el.dataset.action === 'select-folder') selectFolder(Number(el.dataset.folderId));
});

// Folder view — event delegation for note cards
document.getElementById('folder-view-grid').addEventListener('click', function(e) {
  const el = e.target.closest('[data-action="open-note"]');
  if (!el) return;
  openNote(Number(el.dataset.noteId));
});

// Right-click context menu on note items in the tree
let ctxNoteId = null, ctxNoteTitle = null;

function hideNoteCtxMenu() {
  document.getElementById('note-ctx-menu').style.display = 'none';
}

document.getElementById('tree').addEventListener('contextmenu', function(e) {
  const el = e.target.closest('[data-action="open-note"]');
  if (!el) return;
  e.preventDefault();
  ctxNoteId = Number(el.dataset.noteId);
  ctxNoteTitle = el.title;
  const menu = document.getElementById('note-ctx-menu');
  menu.style.display = 'block';
  // Keep menu inside viewport
  const mw = 140, mh = 80;
  menu.style.left = Math.min(e.clientX, window.innerWidth - mw) + 'px';
  menu.style.top = Math.min(e.clientY, window.innerHeight - mh) + 'px';
});

document.getElementById('note-ctx-menu').addEventListener('click', async function(e) {
  const btn = e.target.closest('[data-ctx-action]');
  if (!btn) return;
  hideNoteCtxMenu();
  if (btn.dataset.ctxAction === 'open') openNote(ctxNoteId);
  if (btn.dataset.ctxAction === 'rename') {
    const newTitle = prompt(`Rename note "${ctxNoteTitle}":`, ctxNoteTitle);
    if (!newTitle || newTitle.trim() === ctxNoteTitle) return;
    try {
      await api('PATCH', `/api/v1/notes/${ctxNoteId}`, { title: newTitle.trim() });
      if (currentNote && currentNote.id === ctxNoteId) {
        currentNote.title = newTitle.trim();
        document.getElementById('note-title').value = newTitle.trim();
        document.title = 'thornotes \u2014 ' + newTitle.trim();
      }
      await loadFolderTree();
    } catch (err) {
      showNotification(err.message || 'Failed to rename note', true);
    }
  }
  if (btn.dataset.ctxAction === 'delete') deleteNote(ctxNoteId, ctxNoteTitle);
});

document.addEventListener('click', hideNoteCtxMenu);

// Returns true when the focused element is a text input, select, or contenteditable
// (i.e. the CM6 editor, the title field, the tags field, or any modal input).
function isTextInputFocused() {
  const el = document.activeElement;
  if (!el || el === document.body) return false;
  const tag = el.tagName.toLowerCase();
  return tag === 'input' || tag === 'textarea' || tag === 'select' || el.isContentEditable;
}

document.addEventListener('keydown', function(e) {
  if (e.key === 'Escape') { hideNoteCtxMenu(); hideFolderCtxMenu(); hideEditorCtxMenu(); hideTablePasteBar(); hideTablePicker(); return; }
  // Delete key: delete the currently open note, but never while typing.
  if (e.key === 'Delete' && !e.shiftKey && !e.ctrlKey && !e.metaKey) {
    if (isTextInputFocused()) return;
    if (currentNote) {
      e.preventDefault();
      deleteNote(currentNote.id, currentNote.title);
    }
  }
});

// Right-click context menu on folder labels in the tree
let ctxFolderId = null, ctxFolderName = null;

function hideFolderCtxMenu() {
  document.getElementById('folder-ctx-menu').style.display = 'none';
}

document.getElementById('tree').addEventListener('contextmenu', function(e) {
  const el = e.target.closest('[data-action="select-folder"]');
  if (!el) return;
  e.preventDefault();
  ctxFolderId = Number(el.dataset.folderId);
  ctxFolderName = el.title || el.textContent.trim();
  const menu = document.getElementById('folder-ctx-menu');
  menu.style.display = 'block';
  const mw = 140, mh = 80;
  menu.style.left = Math.min(e.clientX, window.innerWidth - mw) + 'px';
  menu.style.top = Math.min(e.clientY, window.innerHeight - mh) + 'px';
});

document.getElementById('folder-ctx-menu').addEventListener('click', async function(e) {
  const btn = e.target.closest('[data-folder-ctx-action]');
  if (!btn) return;
  hideFolderCtxMenu();
  if (btn.dataset.folderCtxAction === 'rename') {
    const newName = prompt(`Rename folder "${ctxFolderName}":`, ctxFolderName);
    if (!newName || newName.trim() === ctxFolderName) return;
    try {
      await api('PATCH', `/api/v1/folders/${ctxFolderId}`, { name: newName.trim() });
      await loadFolderTree();
    } catch (err) {
      showNotification(err.message || 'Failed to rename folder', true);
    }
  }
  if (btn.dataset.folderCtxAction === 'delete') {
    if (!confirm(`Delete folder "${ctxFolderName}" and all its contents?\n\nThis cannot be undone.`)) return;
    try {
      await api('DELETE', `/api/v1/folders/${ctxFolderId}`);
      if (currentFolderId === ctxFolderId) {
        currentFolderId = null;
        closeFolderView();
      }
      await loadFolderTree();
    } catch (err) {
      showNotification(err.message || 'Failed to delete folder', true);
    }
  }
});

document.addEventListener('click', hideFolderCtxMenu);

// Token list — event delegation for dynamically rendered revoke buttons
document.getElementById('token-list').addEventListener('click', function(e) {
  const btn = e.target.closest('[data-action="revoke-token"]');
  if (btn) revokeToken(Number(btn.dataset.tokenId));
});

// History modal
document.getElementById('history-btn').addEventListener('click', openHistoryModal);
document.getElementById('history-modal').addEventListener('click', function(e) { if (e.target === this) closeHistoryModal(); });
document.getElementById('history-cancel-btn').addEventListener('click', closeHistoryModal);
document.getElementById('history-restore-btn').addEventListener('click', restoreHistoryVersion);
document.getElementById('history-list').addEventListener('click', function(e) {
  const entry = e.target.closest('.history-entry');
  if (entry) selectHistoryEntry(entry.dataset.sha);
});

// Import modal
document.getElementById('import-btn').addEventListener('click', function() {
  document.getElementById('import-file-input').value = '';
  document.getElementById('import-status').textContent = '';
  document.getElementById('import-modal').style.display = 'flex';
});

document.getElementById('import-modal').addEventListener('click', function(e) { if (e.target === this) closeImportModal(); });
document.getElementById('import-cancel-btn').addEventListener('click', closeImportModal);

function closeImportModal() {
  document.getElementById('import-modal').style.display = 'none';
}

document.getElementById('import-confirm-btn').addEventListener('click', async function() {
  const fileInput = document.getElementById('import-file-input');
  const statusEl = document.getElementById('import-status');
  if (!fileInput.files || fileInput.files.length === 0) {
    statusEl.style.color = '#c00';
    statusEl.textContent = 'Please select a file.';
    return;
  }
  const file = fileInput.files[0];
  const formData = new FormData();
  formData.append('file', file);
  statusEl.style.color = 'var(--text-muted)';
  statusEl.textContent = 'Importing…';
  this.disabled = true;
  try {
    const res = await fetch('/api/v1/import', {
      method: 'POST',
      headers: { 'X-CSRF-Token': csrfToken },
      body: formData,
    });
    const body = await res.json();
    if (!res.ok) throw new Error(body.error || 'Import failed');
    statusEl.style.color = '#2e7d32';
    statusEl.textContent = `Imported ${body.notes_created} note${body.notes_created !== 1 ? 's' : ''}` +
      (body.folders_created ? ` and ${body.folders_created} folder${body.folders_created !== 1 ? 's' : ''}` : '') + '.';
    await loadFolderTree();
  } catch (err) {
    statusEl.style.color = '#c00';
    statusEl.textContent = err.message || 'Import failed.';
  } finally {
    this.disabled = false;
  }
});

// Disk full banner
document.getElementById('disk-full-dismiss').addEventListener('click', function() { document.getElementById('disk-full-banner').style.display = 'none'; });

// ── Table feature ──────────────────────────────────────────────────────────

// Hide the paste conversion bar and clear pending state.
function hideTablePasteBar() {
  _pendingTablePaste = null;
  const bar = document.getElementById('table-paste-bar');
  if (bar) bar.style.display = 'none';
}

// ── Table size picker ─────────────────────────────────────────────────────────

const TSP_COLS = 8;
const TSP_ROWS = 8;

(function initTablePicker() {
  const grid = document.getElementById('tsp-grid');
  if (!grid) return;
  for (let r = 1; r <= TSP_ROWS; r++) {
    for (let c = 1; c <= TSP_COLS; c++) {
      const cell = document.createElement('div');
      cell.className = 'tsp-cell';
      cell.dataset.row = r;
      cell.dataset.col = c;
      grid.appendChild(cell);
    }
  }
})();

function hideTablePicker() {
  const picker = document.getElementById('table-size-picker');
  if (picker) picker.style.display = 'none';
}

function showTablePicker(anchorBtn) {
  const picker = document.getElementById('table-size-picker');
  if (!picker) return;
  // Reset highlight to 1×1
  updateTablePickerHighlight(1, 1);
  // Position below the anchor button
  const rect = anchorBtn.getBoundingClientRect();
  picker.style.display = 'block';
  const pw = picker.offsetWidth;
  const ph = picker.offsetHeight;
  let left = rect.left;
  let top = rect.bottom + 4;
  if (left + pw > window.innerWidth - 8) left = window.innerWidth - pw - 8;
  if (top + ph > window.innerHeight - 8) top = rect.top - ph - 4;
  picker.style.left = left + 'px';
  picker.style.top = top + 'px';
}

function updateTablePickerHighlight(rows, cols) {
  const grid = document.getElementById('tsp-grid');
  const label = document.getElementById('tsp-label');
  if (!grid) return;
  grid.querySelectorAll('.tsp-cell').forEach(function(cell) {
    const r = +cell.dataset.row;
    const c = +cell.dataset.col;
    cell.classList.toggle('active', r <= rows && c <= cols);
  });
  if (label) label.textContent = cols + ' \u00d7 ' + rows;
}

function insertTableTemplate(rows, cols) {
  if (!editor) return;
  hideTablePicker();
  const view = editor._view;
  const sel = view.state.selection.main;
  const lineStart = view.state.doc.lineAt(sel.from).from;
  const prefix = sel.from === lineStart ? '' : '\n';
  const header = '| ' + Array.from({ length: cols }, function(_, i) { return 'Col ' + (i + 1); }).join(' | ') + ' |';
  const sep    = '| ' + Array.from({ length: cols }, function() { return '--------'; }).join(' | ') + ' |';
  const row    = '| ' + Array.from({ length: cols }, function() { return 'Cell    '; }).join(' | ') + ' |';
  const lines  = [header, sep].concat(Array.from({ length: rows }, function() { return row; }));
  const tpl = prefix + lines.join('\n') + '\n';
  const cursorAt = sel.from + prefix.length + 2;
  view.dispatch({
    changes: { from: sel.from, to: sel.to, insert: tpl },
    selection: { anchor: cursorAt, head: cursorAt + ('Col 1').length },
  });
  view.focus();
}

document.getElementById('tsp-grid').addEventListener('mouseover', function(e) {
  const cell = e.target.closest('.tsp-cell');
  if (!cell) return;
  updateTablePickerHighlight(+cell.dataset.row, +cell.dataset.col);
});

document.getElementById('tsp-grid').addEventListener('click', function(e) {
  const cell = e.target.closest('.tsp-cell');
  if (!cell) return;
  insertTableTemplate(+cell.dataset.row, +cell.dataset.col);
});

function handleTableBtn(e, btn) {
  e.stopPropagation();
  // If there is a tabular selection, convert it immediately.
  if (editor) {
    const view = editor._view;
    const sel = view.state.selection.main;
    if (!sel.empty) {
      const text = view.state.doc.sliceString(sel.from, sel.to);
      if (parseTabularText(text)) {
        selectionToTable();
        return;
      }
    }
  }
  const picker = document.getElementById('table-size-picker');
  if (picker && picker.style.display !== 'none') {
    hideTablePicker();
  } else {
    showTablePicker(btn);
  }
}

// Replace the just-pasted raw text with its Markdown table equivalent.
function convertPasteToTable() {
  if (!_pendingTablePaste || !editor) return;
  const { from, text, rows } = _pendingTablePaste;
  // Clear state BEFORE dispatching so onEditorChange doesn't re-hide the bar.
  _pendingTablePaste = null;
  hideTablePasteBar();
  const table = rowsToMarkdownTable(rows);
  editor._view.dispatch({
    changes: { from, to: from + text.length, insert: table },
  });
  editor._view.focus();
}

// Replace the current editor selection with a Markdown table.
// Called from the editor context menu and (when selection is tabular) the toolbar.
function selectionToTable() {
  if (!editor) return;
  const view = editor._view;
  const sel = view.state.selection.main;
  if (sel.empty) return;
  const text = view.state.doc.sliceString(sel.from, sel.to);
  const rows = parseTabularText(text);
  if (!rows) {
    showNotification('Selection is not CSV/TSV — nothing to convert', true);
    return;
  }
  view.dispatch({
    changes: { from: sel.from, to: sel.to, insert: rowsToMarkdownTable(rows) },
  });
  view.focus();
}

// Hide the editor context menu.
function hideEditorCtxMenu() {
  const m = document.getElementById('editor-ctx-menu');
  if (m) m.style.display = 'none';
}

// Editor context menu click handler.
document.getElementById('editor-ctx-menu').addEventListener('click', function(e) {
  const btn = e.target.closest('[data-editor-ctx-action]');
  if (!btn) return;
  hideEditorCtxMenu();
  const action = btn.dataset.editorCtxAction;
  if (action === 'to-table') { selectionToTable(); return; }
  if (action === 'formatTable') { formatTableAtCursor(); return; }
  if (CM6.commands[action]) { CM6.commands[action](editor); return; }
});

document.addEventListener('click', hideEditorCtxMenu);
document.addEventListener('click', function(e) {
  if (!e.target.closest('#table-size-picker') && !e.target.closest('#cm6-table-btn')) {
    hideTablePicker();
  }
});

// ── CM6 extra commands ─────────────────────────────────────────────────────
// Extend the vendor bundle's command map with commands that require app context.
CM6.commands.table = function(ed) {
  const view = ed._view;
  const { state } = view;
  const sel = state.selection.main;

  // If there's a selection and it looks tabular, convert it.
  if (!sel.empty) {
    const text = state.doc.sliceString(sel.from, sel.to);
    const rows = parseTabularText(text);
    if (rows) {
      view.dispatch({
        changes: { from: sel.from, to: sel.to, insert: rowsToMarkdownTable(rows) },
      });
      view.focus();
      return;
    }
  }

  // No selection (or non-tabular selection): insert a blank template.
  const lineStart = state.doc.lineAt(sel.from).from;
  const prefix = sel.from === lineStart ? '' : '\n';
  const tpl = prefix + '| Header 1 | Header 2 | Header 3 |\n| -------- | -------- | -------- |\n| Cell     | Cell     | Cell     |\n';
  const cursorAt = sel.from + prefix.length + 2;
  view.dispatch({
    changes: { from: sel.from, to: sel.to, insert: tpl },
    selection: { anchor: cursorAt, head: cursorAt + 8 }, // pre-select "Header 1"
  });
  view.focus();
};

// Settings modal
document.getElementById('settings-modal').addEventListener('click', function(e) { if (e.target === this) closeSettings(); });
document.getElementById('settings-done-btn').addEventListener('click', closeSettings);
document.getElementById('theme-select').addEventListener('change', function() { applyTheme(this.value); });
document.getElementById('auto-collapse-toggle').addEventListener('change', function() {
  localStorage.setItem('autoCollapse', this.checked ? 'true' : 'false');
  if (this.checked) _resetAutoCollapseTimer();
  else if (_autoCollapseTimer) { clearTimeout(_autoCollapseTimer); _autoCollapseTimer = null; }
});
document.getElementById('auto-lint-toggle').addEventListener('change', function() {
  localStorage.setItem('autoLint', this.checked ? 'true' : 'false');
  applyLintSetting();
});

// Browser back/forward — reopen the note recorded in history state.
window.addEventListener('popstate', function(e) {
  if (e.state && e.state.noteId) {
    openNote(e.state.noteId, { historyMode: 'none' });
  }
});
