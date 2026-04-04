'use strict';
// Preload script — runs in a sandboxed renderer context before any page scripts.
// Exposes a minimal, typed surface to the renderer via contextBridge.
// Nothing from Node / Electron internals leaks through.

const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('thornotes', {
  /** Return the currently saved config (serverUrl, etc.) */
  getConfig: () => ipcRenderer.invoke('tn:get-config'),

  /** Persist config and open the main window at the new URL. */
  saveConfig: (cfg) => ipcRenderer.invoke('tn:save-config', cfg),

  /** Ask the main process to reload the main window at the saved URL. */
  reconnect: () => ipcRenderer.invoke('tn:reconnect'),

  /** Platform string for platform-specific hints in the UI. */
  platform: process.platform,
});
