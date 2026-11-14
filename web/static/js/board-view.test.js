/* thornotes — board-view test suite
   Unit tests for the pure board grouping functions. */
'use strict';

const { describe, it } = require('node:test');
const assert = require('node:assert/strict');
const {
  buildBoard,
  columnOrder,
  humaniseStatus,
  titleCaseProject,
  extractPriority,
  extractStatus,
  extractProject,
  KNOWN_STATUSES,
} = require('./board-view.js');

// ── columnOrder ──────────────────────────────────────────────────────────────

describe('columnOrder', () => {
  it('returns known statuses in fixed order', () => {
    const result = columnOrder(['done', 'todo', 'running', 'blocked']);
    assert.deepEqual(result, ['todo', 'running', 'blocked', 'done']);
  });

  it('places unknown statuses after known ones, alphabetically', () => {
    const result = columnOrder(['on-fire', 'todo', 'zoo-mode', 'in-review', 'alpha']);
    assert.deepEqual(result, ['todo', 'in-review', 'alpha', 'on-fire', 'zoo-mode']);
  });

  it('deduplicates statuses', () => {
    const result = columnOrder(['todo', 'todo', 'running', 'todo']);
    assert.deepEqual(result, ['todo', 'running']);
  });

  it('handles empty input', () => {
    assert.deepEqual(columnOrder([]), []);
  });

  it('handles only unknown statuses', () => {
    const result = columnOrder(['zebra', 'alpha', 'mid']);
    assert.deepEqual(result, ['alpha', 'mid', 'zebra']);
  });

  it('includes all known statuses in correct order when all present', () => {
    const result = columnOrder(['archived', 'done', 'blocked', 'in-review', 'running', 'ready', 'todo']);
    assert.deepEqual(result, ['todo', 'ready', 'running', 'in-review', 'blocked', 'done', 'archived']);
  });
});

// ── humaniseStatus ───────────────────────────────────────────────────────────

describe('humaniseStatus', () => {
  it('capitalises first letter and replaces hyphens with spaces', () => {
    assert.equal(humaniseStatus('in-review'), 'In review');
  });

  it('handles single-word statuses', () => {
    assert.equal(humaniseStatus('todo'), 'Todo');
    assert.equal(humaniseStatus('done'), 'Done');
  });

  it('handles multi-hyphen statuses', () => {
    assert.equal(humaniseStatus('on-hold-review'), 'On hold review');
  });
});

// ── titleCaseProject ─────────────────────────────────────────────────────────

describe('titleCaseProject', () => {
  it('capitalises first letter', () => {
    assert.equal(titleCaseProject('thornotes'), 'Thornotes');
  });

  it('preserves rest of string', () => {
    assert.equal(titleCaseProject('my-app'), 'My-app');
  });

  it('handles empty string', () => {
    assert.equal(titleCaseProject(''), '');
  });
});

// ── extractPriority ──────────────────────────────────────────────────────────

describe('extractPriority', () => {
  it('returns priority number from tag', () => {
    assert.equal(extractPriority(['status-todo', 'priority-p60', 'project-x']), 60);
  });

  it('returns null when no priority tag', () => {
    assert.equal(extractPriority(['status-todo', 'project-x']), null);
  });

  it('handles single-digit priority', () => {
    assert.equal(extractPriority(['priority-p5']), 5);
  });
});

// ── extractStatus ────────────────────────────────────────────────────────────

describe('extractStatus', () => {
  it('returns first status suffix', () => {
    assert.equal(extractStatus(['project-x', 'status-running', 'priority-p10']), 'running');
  });

  it('returns null when no status tag', () => {
    assert.equal(extractStatus(['project-x', 'priority-p10']), null);
  });
});

// ── extractProject ───────────────────────────────────────────────────────────

describe('extractProject', () => {
  it('returns alphabetically first project suffix', () => {
    assert.equal(extractProject(['status-todo', 'project-zebra', 'project-alpha']), 'alpha');
  });

  it('returns null when no project tag', () => {
    assert.equal(extractProject(['status-todo', 'priority-p10']), null);
  });

  it('returns single project', () => {
    assert.equal(extractProject(['project-thornotes']), 'thornotes');
  });
});

// ── buildBoard ───────────────────────────────────────────────────────────────

describe('buildBoard', () => {
  it('excludes notes without a status tag', () => {
    const notes = [
      { id: 1, title: 'No status', tags: ['project-x'], updated_at: '2026-01-01T00:00:00Z' },
      { id: 2, title: 'Has status', tags: ['status-todo', 'project-x'], updated_at: '2026-01-01T00:00:00Z' },
    ];
    const board = buildBoard(notes);
    assert.equal(board.totalCards, 1);
    assert.equal(board.excludedNoStatus, 1);
    // The 'Has status' note should be in the 'todo' column
    const todoCol = board.columns.find(c => c.status === 'todo');
    assert.ok(todoCol);
    assert.equal(todoCol.cards.length, 1);
    assert.equal(todoCol.cards[0].note.id, 2);
  });

  it('places note with status but no project into "No project" lane', () => {
    const notes = [
      { id: 1, title: 'Orphan', tags: ['status-blocked'], updated_at: '2026-01-01T00:00:00Z' },
    ];
    const board = buildBoard(notes);
    assert.equal(board.totalCards, 1);
    const blockedCol = board.columns.find(c => c.status === 'blocked');
    assert.ok(blockedCol);
    assert.equal(blockedCol.cards.length, 1);
    assert.equal(blockedCol.cards[0].project, '__no_project__');
    assert.equal(blockedCol.cards[0].projectLabel, 'No project');
    // 'No project' lane should exist
    const noProjectLane = board.lanes.find(l => l.project === '__no_project__');
    assert.ok(noProjectLane);
    assert.equal(noProjectLane.label, 'No project');
  });

  it('multi-project note appears under alphabetically first project only', () => {
    const notes = [
      { id: 1, title: 'Multi', tags: ['status-todo', 'project-zebra', 'project-alpha'], updated_at: '2026-01-01T00:00:00Z' },
    ];
    const board = buildBoard(notes);
    assert.equal(board.totalCards, 1);
    const todoCol = board.columns.find(c => c.status === 'todo');
    assert.equal(todoCol.cards[0].project, 'alpha');
    assert.equal(todoCol.cards[0].projectLabel, 'Alpha');
  });

  it('sorts cards by priority descending, then title ascending', () => {
    const notes = [
      { id: 1, title: 'Bravo', tags: ['status-todo', 'priority-p30'], updated_at: '2026-01-01T00:00:00Z' },
      { id: 2, title: 'Alpha', tags: ['status-todo', 'priority-p60'], updated_at: '2026-01-01T00:00:00Z' },
      { id: 3, title: 'Charlie', tags: ['status-todo', 'priority-p30'], updated_at: '2026-01-01T00:00:00Z' },
      { id: 4, title: 'Delta', tags: ['status-todo'], updated_at: '2026-01-01T00:00:00Z' },
    ];
    const board = buildBoard(notes);
    const todoCol = board.columns.find(c => c.status === 'todo');
    const titles = todoCol.cards.map(c => c.note.title);
    // P60 first, then P30 (Alpha before Charlie), then no-priority (Delta)
    assert.deepEqual(titles, ['Alpha', 'Bravo', 'Charlie', 'Delta']);
  });

  it('includes all known status columns even when empty', () => {
    const board = buildBoard([]);
    assert.equal(board.columns.length, KNOWN_STATUSES.length);
    assert.deepEqual(board.columns.map(c => c.status), KNOWN_STATUSES);
    for (const col of board.columns) {
      assert.equal(col.cards.length, 0);
    }
  });

  it('adds unknown status columns after known ones', () => {
    const notes = [
      { id: 1, title: 'On fire', tags: ['status-on-fire', 'project-x'], updated_at: '2026-01-01T00:00:00Z' },
    ];
    const board = buildBoard(notes);
    const lastCol = board.columns[board.columns.length - 1];
    assert.equal(lastCol.status, 'on-fire');
    assert.equal(lastCol.label, 'On fire');
    assert.equal(lastCol.cards.length, 1);
  });

  it('handles empty input', () => {
    const board = buildBoard([]);
    assert.equal(board.totalCards, 0);
    assert.equal(board.excludedNoStatus, 0);
    assert.equal(board.columns.length, KNOWN_STATUSES.length);
    assert.equal(board.lanes.length, 0);
  });

  it('handles null/undefined input gracefully', () => {
    const board = buildBoard(null);
    assert.equal(board.totalCards, 0);
  });

  it('lanes are alphabetical with No project last', () => {
    const notes = [
      { id: 1, title: 'A', tags: ['status-todo', 'project-zebra'], updated_at: '2026-01-01T00:00:00Z' },
      { id: 2, title: 'B', tags: ['status-todo', 'project-alpha'], updated_at: '2026-01-01T00:00:00Z' },
      { id: 3, title: 'C', tags: ['status-todo'], updated_at: '2026-01-01T00:00:00Z' },
    ];
    const board = buildBoard(notes);
    const laneLabels = board.lanes.map(l => l.label);
    assert.deepEqual(laneLabels, ['Alpha', 'Zebra', 'No project']);
  });

  it('totalCards equals sum of all column card counts', () => {
    const notes = [
      { id: 1, title: 'A', tags: ['status-todo', 'project-x'], updated_at: '2026-01-01T00:00:00Z' },
      { id: 2, title: 'B', tags: ['status-done', 'project-x'], updated_at: '2026-01-01T00:00:00Z' },
      { id: 3, title: 'C', tags: ['status-running'], updated_at: '2026-01-01T00:00:00Z' },
      { id: 4, title: 'D', tags: ['project-x'], updated_at: '2026-01-01T00:00:00Z' }, // excluded
    ];
    const board = buildBoard(notes);
    const sum = board.columns.reduce((acc, c) => acc + c.cards.length, 0);
    assert.equal(board.totalCards, sum);
    assert.equal(board.totalCards, 3);
    assert.equal(board.excludedNoStatus, 1);
  });

  it('note with only project tag does not appear on the board', () => {
    const notes = [
      { id: 1, title: 'Only project', tags: ['project-x'], updated_at: '2026-01-01T00:00:00Z' },
    ];
    const board = buildBoard(notes);
    assert.equal(board.totalCards, 0);
    assert.equal(board.excludedNoStatus, 1);
  });

  it('note with status-blocked appears under Blocked column', () => {
    const notes = [
      { id: 1, title: 'Blocked note', tags: ['status-blocked', 'project-x'], updated_at: '2026-01-01T00:00:00Z' },
    ];
    const board = buildBoard(notes);
    const blockedCol = board.columns.find(c => c.status === 'blocked');
    assert.ok(blockedCol);
    assert.equal(blockedCol.cards.length, 1);
    assert.equal(blockedCol.cards[0].note.title, 'Blocked note');
  });

  it('synthetic status-on-fire appears in "On fire" column after known ones', () => {
    const notes = [
      { id: 1, title: 'Burning', tags: ['status-on-fire', 'project-x'], updated_at: '2026-01-01T00:00:00Z' },
    ];
    const board = buildBoard(notes);
    // Known statuses come first, then 'on-fire'
    const knownCount = KNOWN_STATUSES.length;
    assert.equal(board.columns.length, knownCount + 1);
    const lastCol = board.columns[knownCount];
    assert.equal(lastCol.status, 'on-fire');
    assert.equal(lastCol.label, 'On fire');
  });
});
