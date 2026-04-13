// Regression tests for CM6 editor history isolation between notes.
//
// Root cause of bug: setValue used view.dispatch({ changes: ... }) which adds
// the note-switch to CM6's undo stack. Ctrl+Z after switching notes would walk
// back to the previous note's content inside the new note.
//
// Fix: setValue now calls view.setState(EditorState.create(...)) which replaces
// the entire editor state (including history) with a fresh one.
//
// These tests exercise the core history mechanics without a DOM by passing a
// lightweight mock to CM6's undo() — which only needs { state, dispatch }.
//
// Run: node --test web/cm6-bundle/index.test.mjs
//      (requires: npm install --prefix web/cm6-bundle)

import { describe, test } from 'node:test';
import assert from 'node:assert/strict';
import { EditorState } from '@codemirror/state';
import { history, undo } from '@codemirror/commands';

// Try to undo on a given state. Returns the doc string after undo, or null if
// there was nothing to undo (undo() returned false / dispatched nothing).
function tryUndo(state) {
  let result = null;
  undo({ state, dispatch: (tr) => { result = tr.state; } });
  return result ? result.doc.toString() : null;
}

describe('CM6 undo history isolation between notes', () => {
  test('BUG (old dispatch): note-switch is undoable and leaks previous content', () => {
    const noteA = 'note A content';
    const noteB = 'note B content';

    // Simulate old setValue: dispatch a replace-all transaction.
    const stateA = EditorState.create({ doc: noteA, extensions: [history()] });
    const stateB = stateA.update({
      changes: { from: 0, to: stateA.doc.length, insert: noteB },
    }).state;

    assert.equal(stateB.doc.toString(), noteB);

    // This is the bug: undo walks back to note A's content.
    const afterUndo = tryUndo(stateB);
    assert.equal(afterUndo, noteA, 'old dispatch leaks note A into note B via undo');
  });

  test('FIX (EditorState.create): fresh state has empty history, undo does nothing', () => {
    const noteA = 'note A content';
    const noteB = 'note B content';

    // Simulate prior note activity so there is real history to potentially leak.
    let stateA = EditorState.create({ doc: noteA, extensions: [history()] });
    stateA = stateA.update({ changes: { from: noteA.length, insert: ' (edited)' } }).state;

    // New setValue: create a brand-new EditorState (history is empty).
    const stateB = EditorState.create({ doc: noteB, extensions: [history()] });

    assert.equal(stateB.doc.toString(), noteB);

    // Undo does nothing — no history to walk back.
    const afterUndo = tryUndo(stateB);
    assert.equal(afterUndo, null, 'fresh state has no undo history');
  });

  test('undo still works for edits made within the current note after switch', () => {
    const noteB = 'note B content';

    // Fresh state as loaded by the fixed setValue.
    const freshState = EditorState.create({ doc: noteB, extensions: [history()] });

    // User makes an edit inside note B.
    const editedState = freshState.update({
      changes: { from: 0, insert: 'prefix: ' },
    }).state;

    assert.equal(editedState.doc.toString(), 'prefix: note B content');

    // Undo reverts only the user's edit, not the note-switch.
    const afterUndo = tryUndo(editedState);
    assert.equal(afterUndo, noteB, 'undo reverts in-note edit back to note B content');
  });

  test('multiple switches: each fresh state is independent', () => {
    const notes = ['alpha', 'beta', 'gamma'];

    // Load alpha, edit it, switch to beta, edit it, switch to gamma.
    let state = EditorState.create({ doc: notes[0], extensions: [history()] });
    state = state.update({ changes: { from: 0, insert: 'edit: ' } }).state;

    state = EditorState.create({ doc: notes[1], extensions: [history()] });
    state = state.update({ changes: { from: 0, insert: 'edit: ' } }).state;

    state = EditorState.create({ doc: notes[2], extensions: [history()] });

    assert.equal(state.doc.toString(), 'gamma');

    // Undo should do nothing (gamma was loaded fresh, no edits yet).
    assert.equal(tryUndo(state), null, 'no history after loading gamma fresh');
  });
});
