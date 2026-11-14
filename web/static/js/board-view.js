/* thornotes — board view utilities
   Pure functions; no DOM dependencies so they run under Node for testing.
   Groups notes (from GET /api/v1/notes/all) into a board: columns are
   status-* tags, swimlanes are project-* tags. */
'use strict';

/**
 * Fixed column order — these statuses always appear first, in this order.
 * Any status-* tag not in this list is appended after, sorted alphabetically.
 */
const KNOWN_STATUSES = [
  'todo',
  'ready',
  'running',
  'in-review',
  'blocked',
  'done',
  'archived',
];

/**
 * columnOrder: given an array of status suffixes (strings), return them
 * sorted in the canonical board order: known statuses first (in fixed order),
 * then unknown statuses alphabetically.
 *
 * @param {string[]} statuses - e.g. ['todo', 'running', 'on-fire', 'blocked']
 * @returns {string[]} sorted status suffixes
 */
function columnOrder(statuses) {
  const seen = new Set();
  const unique = [];
  for (const s of statuses) {
    if (!seen.has(s)) { seen.add(s); unique.push(s); }
  }
  const knownIdx = new Map(KNOWN_STATUSES.map((s, i) => [s, i]));
  const known = [];
  const unknown = [];
  for (const s of unique) {
    if (knownIdx.has(s)) known.push(s);
    else unknown.push(s);
  }
  known.sort((a, b) => knownIdx.get(a) - knownIdx.get(b));
  unknown.sort();
  return known.concat(unknown);
}

/**
 * Humanise a status suffix: 'in-review' → 'In review', 'todo' → 'Todo'.
 * First letter capitalised, hyphens replaced with spaces.
 */
function humaniseStatus(suffix) {
  const spaced = suffix.replace(/-/g, ' ');
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

/**
 * Title-case a project suffix: 'thornotes' → 'Thornotes', 'my-app' → 'My-app'.
 * Only the first letter is capitalised; the rest is preserved as-is.
 */
function titleCaseProject(suffix) {
  if (!suffix) return suffix;
  return suffix.charAt(0).toUpperCase() + suffix.slice(1);
}

/**
 * Extract the priority number from a note's tags, or null if none.
 * Priority tags match 'priority-pNN' where NN is a number.
 * @param {string[]} tags
 * @returns {number|null}
 */
function extractPriority(tags) {
  for (const t of tags) {
    const m = t.match(/^priority-p(\d+)$/);
    if (m) return parseInt(m[1], 10);
  }
  return null;
}

/**
 * Extract status suffix from tags (first status-* tag), or null.
 * @param {string[]} tags
 * @returns {string|null}
 */
function extractStatus(tags) {
  for (const t of tags) {
    if (t.startsWith('status-')) return t.slice('status-'.length);
  }
  return null;
}

/**
 * Extract project suffix from tags (alphabetically first project-* tag), or null.
 * @param {string[]} tags
 * @returns {string|null}
 */
function extractProject(tags) {
  const projects = [];
  for (const t of tags) {
    if (t.startsWith('project-')) projects.push(t.slice('project-'.length));
  }
  if (projects.length === 0) return null;
  projects.sort();
  return projects[0];
}

/**
 * buildBoard: group notes into a board structure.
 *
 * Input: array of note objects with at least { id, title, tags, updated_at }.
 * Output: {
 *   columns: [{ status, label, cards: [{ note, priority, project, projectLabel }] }],
 *   lanes: [{ project, label }],  // in alphabetical order, 'No project' last
 *   totalCards: number,
 *   excludedNoStatus: number,
 * }
 *
 * Rules:
 * - Notes without a status-* tag are excluded (not on the board).
 * - Notes with a status but no project-* tag go into 'No project' lane.
 * - Notes with multiple project-* tags appear under the alphabetically first.
 * - Columns are ordered: known statuses first (fixed order), then unknown alphabetically.
 * - Cards within a column are sorted: priority descending, then title ascending.
 * - Empty columns (known statuses with no cards) are still included.
 *
 * @param {Array} notes - array of note objects from /api/v1/notes/all
 * @returns {Object} board structure
 */
function buildBoard(notes) {
  if (!Array.isArray(notes)) notes = [];

  // Phase 1: classify each note
  const statusBuckets = new Map(); // status suffix → array of card data
  const allStatuses = new Set();
  const allProjects = new Set();
  let excludedNoStatus = 0;

  for (const note of notes) {
    const tags = Array.isArray(note.tags) ? note.tags : [];
    const status = extractStatus(tags);
    if (status === null) {
      excludedNoStatus++;
      continue;
    }
    allStatuses.add(status);

    const project = extractProject(tags);
    const projectKey = project || '__no_project__';
    if (project) allProjects.add(project);

    const priority = extractPriority(tags);

    const card = {
      note,
      priority,
      project: projectKey,
      projectLabel: project ? titleCaseProject(project) : 'No project',
    };

    if (!statusBuckets.has(status)) statusBuckets.set(status, []);
    statusBuckets.get(status).push(card);
  }

  // Phase 2: determine column order — always include all known statuses
  for (const ks of KNOWN_STATUSES) allStatuses.add(ks);
  const orderedStatuses = columnOrder(Array.from(allStatuses));

  // Phase 3: sort cards within each column (priority desc, then title asc)
  for (const cards of statusBuckets.values()) {
    cards.sort((a, b) => {
      const pa = a.priority != null ? a.priority : -1;
      const pb = b.priority != null ? b.priority : -1;
      if (pa !== pb) return pb - pa; // descending
      const ta = (a.note.title || '').toLowerCase();
      const tb = (b.note.title || '').toLowerCase();
      if (ta < tb) return -1;
      if (ta > tb) return 1;
      return 0;
    });
  }

  // Phase 4: build columns
  const columns = orderedStatuses.map(status => ({
    status,
    label: humaniseStatus(status),
    cards: statusBuckets.get(status) || [],
  }));

  // Phase 5: build lane list (alphabetical, 'No project' last)
  const laneList = Array.from(allProjects).sort().map(p => ({
    project: p,
    label: titleCaseProject(p),
  }));

  // Check if any card uses 'No project'
  let hasNoProject = false;
  for (const cards of statusBuckets.values()) {
    for (const c of cards) {
      if (c.project === '__no_project__') { hasNoProject = true; break; }
    }
    if (hasNoProject) break;
  }
  if (hasNoProject) {
    laneList.push({ project: '__no_project__', label: 'No project' });
  }

  // Phase 6: count total cards
  let totalCards = 0;
  for (const cards of statusBuckets.values()) totalCards += cards.length;

  return { columns, lanes: laneList, totalCards, excludedNoStatus };
}

// CommonJS export for the Node.js test runner — no-op in browsers.
if (typeof module !== 'undefined') {
  module.exports = {
    buildBoard,
    columnOrder,
    humaniseStatus,
    titleCaseProject,
    extractPriority,
    extractStatus,
    extractProject,
    KNOWN_STATUSES,
  };
}
