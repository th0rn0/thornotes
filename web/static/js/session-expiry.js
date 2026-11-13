/* Session expiry state machine - extracted from app.js
   Pure functions with no DOM dependencies. */
'use strict';

function shouldExpireSession(status, currentUser, sessionExpired) {
  return status === 401 && !!currentUser && !sessionExpired;
}

function canScheduleAutoSave(sessionExpired) {
  return !sessionExpired;
}

function canAutoSave(sessionExpired, currentNote) {
  return !sessionExpired && !!currentNote;
}

function computeSaveStatusOnSuccess() {
  return 'saved';
}

function computeSaveStatusOnError() {
  return 'error';
}

if (typeof module !== 'undefined' && module.exports) {
  module.exports = {
    shouldExpireSession,
    canScheduleAutoSave,
    canAutoSave,
    computeSaveStatusOnSuccess,
    computeSaveStatusOnError,
  };
}
