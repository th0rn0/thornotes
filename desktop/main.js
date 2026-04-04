'use strict';
// thornotes desktop — main process
// Wraps any thornotes server (local or remote) in a native window.
// Config is stored in the OS user-data directory so it survives app updates.

const {
  app, BrowserWindow, Tray, Menu, nativeImage,
  ipcMain, shell,
} = require('electron');
const path = require('path');
const fs   = require('fs');
const { validateServerUrl, mergeConfig } = require('./lib.js');

// ── Config ────────────────────────────────────────────────────────────────────
const CONFIG_FILE = path.join(app.getPath('userData'), 'config.json');

function loadConfig() {
  try { return JSON.parse(fs.readFileSync(CONFIG_FILE, 'utf8')); }
  catch { return {}; }
}

function persistConfig(data) {
  const dir = path.dirname(CONFIG_FILE);
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(CONFIG_FILE, JSON.stringify(data, null, 2), 'utf8');
}

// ── State ─────────────────────────────────────────────────────────────────────
let mainWin  = null;
let setupWin = null;
let tray     = null;
let config   = loadConfig();

// ── Icon helpers ──────────────────────────────────────────────────────────────
function assetPath(name) {
  const p = path.join(__dirname, 'assets', name);
  return fs.existsSync(p) ? p : null;
}

// Build a 16×16 tray icon.  Falls back to a coloured dot drawn into a
// NativeImage if no icon file is present, so the tray always has something.
function makeTrayIcon() {
  const p = assetPath(process.platform === 'win32' ? 'tray-icon.ico' : 'tray-icon.png');
  if (p) {
    const img = nativeImage.createFromPath(p);
    return img.resize({ width: 16, height: 16 });
  }
  // Fallback: 1×1 transparent PNG — tray entry still appears.
  return nativeImage.createFromDataURL(
    'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=='
  );
}

// ── Window factories ──────────────────────────────────────────────────────────
function createSetupWindow(error = null) {
  if (setupWin) { setupWin.focus(); return; }

  setupWin = new BrowserWindow({
    width: 440,
    height: 360,
    resizable: false,
    minimizable: false,
    maximizable: false,
    fullscreenable: false,
    title: 'thornotes — Connect',
    autoHideMenuBar: true,
    webPreferences: {
      preload: path.join(__dirname, 'preload.js'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  });

  const icon = assetPath('icon.png');
  if (icon) setupWin.setIcon(nativeImage.createFromPath(icon));

  const query = {};
  if (error)            query.error      = error;
  if (config.serverUrl) query.serverUrl  = config.serverUrl;

  setupWin.loadFile(path.join(__dirname, 'setup.html'), { query });
  setupWin.on('closed', () => { setupWin = null; });
}

function createMainWindow(url) {
  if (mainWin) {
    mainWin.loadURL(url);
    mainWin.show();
    mainWin.focus();
    return;
  }

  mainWin = new BrowserWindow({
    width: 1280,
    height: 840,
    minWidth: 480,
    minHeight: 400,
    title: 'thornotes',
    autoHideMenuBar: true,
    webPreferences: {
      preload: path.join(__dirname, 'preload.js'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: false, // needs false so preload can use ipcRenderer in the notes app context
      // Allow the renderer to load the configured server URL
      webSecurity: true,
    },
  });

  const icon = assetPath('icon.png');
  if (icon) mainWin.setIcon(nativeImage.createFromPath(icon));

  mainWin.loadURL(url);

  // Keep title in sync with the page title.
  mainWin.webContents.on('page-title-updated', (_, title) => {
    if (title) mainWin.setTitle(title);
  });

  // Open target="_blank" / external links in the system browser.
  mainWin.webContents.setWindowOpenHandler(({ url: targetUrl }) => {
    shell.openExternal(targetUrl);
    return { action: 'deny' };
  });

  // Connection failure — switch to setup screen.
  mainWin.webContents.on('did-fail-load', (_, code, desc, failedUrl) => {
    // -3 = ABORTED (user navigated, ignore)
    if (code === -3 || !failedUrl || failedUrl === 'about:blank') return;
    createSetupWindow(`Could not reach ${failedUrl} (${desc})`);
    mainWin.close();
  });

  mainWin.on('closed', () => { mainWin = null; });
}

// ── Tray ──────────────────────────────────────────────────────────────────────
function buildTrayMenu() {
  return Menu.buildFromTemplate([
    {
      label: 'Open thornotes',
      click() {
        if (mainWin)            { mainWin.show(); mainWin.focus(); }
        else if (config.serverUrl) createMainWindow(config.serverUrl);
        else                       createSetupWindow();
      },
    },
    {
      label: 'Open in browser',
      enabled: Boolean(config.serverUrl),
      click() { if (config.serverUrl) shell.openExternal(config.serverUrl); },
    },
    { type: 'separator' },
    { label: 'Change server…', click() { createSetupWindow(); } },
    { type: 'separator' },
    { label: 'Quit thornotes', role: 'quit' },
  ]);
}

function initTray() {
  tray = new Tray(makeTrayIcon());
  tray.setToolTip('thornotes');
  tray.setContextMenu(buildTrayMenu());
  // Double-click on tray icon shows the app (Windows / Linux).
  tray.on('double-click', () => {
    if (mainWin)            { mainWin.show(); mainWin.focus(); }
    else if (config.serverUrl) createMainWindow(config.serverUrl);
    else                       createSetupWindow();
  });
}

function refreshTray() {
  if (tray) tray.setContextMenu(buildTrayMenu());
}

// ── IPC handlers ──────────────────────────────────────────────────────────────
ipcMain.handle('tn:get-config', () => ({
  serverUrl: config.serverUrl || '',
}));

ipcMain.handle('tn:save-config', (_, { serverUrl }) => {
  const result = validateServerUrl(serverUrl);
  if (!result.ok) return result;

  config = mergeConfig(config, { serverUrl: result.url });
  persistConfig(config);
  refreshTray();

  // Navigate to the saved URL.
  if (setupWin) { setupWin.close(); setupWin = null; }
  createMainWindow(result.url);

  return { ok: true };
});

ipcMain.handle('tn:reconnect', () => {
  if (config.serverUrl) createMainWindow(config.serverUrl);
  else createSetupWindow();
});

// ── Lifecycle ─────────────────────────────────────────────────────────────────
app.whenReady().then(() => {
  initTray();

  if (config.serverUrl) {
    createMainWindow(config.serverUrl);
  } else {
    createSetupWindow();
  }
});

// macOS: clicking the dock icon re-opens the window.
app.on('activate', () => {
  if (mainWin)            { mainWin.show(); mainWin.focus(); }
  else if (config.serverUrl) createMainWindow(config.serverUrl);
  else                       createSetupWindow();
});

// Keep running in the system tray when the last window is closed.
// The user can quit via the tray menu.
app.on('window-all-closed', () => {
  // Intentionally do nothing — stay alive in the tray.
});
