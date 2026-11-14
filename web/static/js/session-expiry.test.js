/* Real tests for session-expiry module - no mirrors */
const { test } = require('node:test');
const assert = require('node:assert');
const {
  shouldExpireSession,
  canScheduleAutoSave,
  canAutoSave,
  computeSaveStatusOnSuccess,
  computeSaveStatusOnError,
} = require('./session-expiry.js');

test('shouldExpireSession returns true only when 401 with user and not already expired', () => {
  assert.strictEqual(shouldExpireSession(401, { id: 1 }, false), true);
  assert.strictEqual(shouldExpireSession(401, null, false), false);
  assert.strictEqual(shouldExpireSession(401, { id: 1 }, true), false);
  assert.strictEqual(shouldExpireSession(200, { id: 1 }, false), false);
});

test('canScheduleAutoSave returns true only when not expired', () => {
  assert.strictEqual(canScheduleAutoSave(false), true);
  assert.strictEqual(canScheduleAutoSave(true), false);
});

test('canAutoSave returns true only when not expired and has note', () => {
  assert.strictEqual(canAutoSave(false, { id: 1 }), true);
  assert.strictEqual(canAutoSave(false, null), false);
  assert.strictEqual(canAutoSave(true, { id: 1 }), false);
  assert.strictEqual(canAutoSave(true, null), false);
});

test('computeSaveStatusOnSuccess returns saved', () => {
  assert.strictEqual(computeSaveStatusOnSuccess(), 'saved');
});

test('computeSaveStatusOnError returns error', () => {
  assert.strictEqual(computeSaveStatusOnError(), 'error');
});
