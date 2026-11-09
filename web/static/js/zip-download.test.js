/* thornotes — zip download helpers
   Unit tests for the pure helpers in zip-download.js. */
'use strict';

const { describe, test } = require('node:test');
const assert = require('node:assert/strict');
const {
  buildZipUrl,
  parseContentDispositionFilename,
  rowExistsInTree,
  buildZipFilename,
} = require('./zip-download.js');

describe('buildZipUrl', () => {
  test('note endpoint', () => {
    assert.equal(buildZipUrl('note', 42), '/api/v1/notes/42/zip');
  });

  test('folder endpoint', () => {
    assert.equal(buildZipUrl('folder', 7), '/api/v1/folders/7/zip');
  });

  test('rejects unknown kind', () => {
    assert.throws(() => buildZipUrl('tag', 1), /kind must be/);
  });

  test('rejects non-positive id', () => {
    assert.throws(() => buildZipUrl('note', 0), /positive number/);
    assert.throws(() => buildZipUrl('note', -3), /positive number/);
    assert.throws(() => buildZipUrl('note', NaN), /positive number/);
  });
});

describe('parseContentDispositionFilename', () => {
  test('returns fallback when header missing', () => {
    assert.equal(parseContentDispositionFilename(undefined, 'note.zip'), 'note.zip');
    assert.equal(parseContentDispositionFilename('', 'note.zip'), 'note.zip');
  });

  test('plain quoted filename', () => {
    assert.equal(
      parseContentDispositionFilename('attachment; filename="hello.zip"', 'fb.zip'),
      'hello.zip'
    );
  });

  test('plain unquoted filename', () => {
    assert.equal(
      parseContentDispositionFilename('attachment; filename=hello.zip', 'fb.zip'),
      'hello.zip'
    );
  });

  test('RFC 5987 UTF-8 form wins over plain form', () => {
    assert.equal(
      parseContentDispositionFilename(
        `attachment; filename="fallback.zip"; filename*=UTF-8''My%20Notes.zip`,
        'fb.zip'
      ),
      'My Notes.zip'
    );
  });

  test('RFC 5987 with unicode escapes', () => {
    assert.equal(
      parseContentDispositionFilename(
        `attachment; filename*=UTF-8''%E6%97%A5%E6%9C%AC%E8%AA%9E.zip`,
        'fb.zip'
      ),
      '日本語.zip'
    );
  });

  test('bad UTF-8 percent encoding falls back to plain form', () => {
    assert.equal(
      parseContentDispositionFilename(
        `attachment; filename="plain.zip"; filename*=UTF-8''%ZZbad`,
        'fb.zip'
      ),
      // decodeURIComponent throws on %ZZ, so the plain form is used
      'plain.zip'
    );
  });

  test('header with no filename uses fallback', () => {
    assert.equal(
      parseContentDispositionFilename('attachment; size=123', 'fb.zip'),
      'fb.zip'
    );
  });
});

describe('rowExistsInTree', () => {
  const folders = [
    { id: 1, name: 'A', parent_id: null },
    { id: 2, name: 'B', parent_id: 1 },
  ];
  const rootNotes = [{ id: 10, title: 'root note' }];
  const notesByFolder = {
    1: [{ id: 11, title: 'n11' }, { id: 12, title: 'n12' }],
    2: [{ id: 21, title: 'n21' }],
  };

  test('finds folder by id', () => {
    assert.equal(rowExistsInTree('folder', 1, folders, rootNotes, notesByFolder), true);
    assert.equal(rowExistsInTree('folder', 2, folders, rootNotes, notesByFolder), true);
  });

  test('missing folder', () => {
    assert.equal(rowExistsInTree('folder', 99, folders, rootNotes, notesByFolder), false);
  });

  test('finds root note', () => {
    assert.equal(rowExistsInTree('note', 10, folders, rootNotes, notesByFolder), true);
  });

  test('finds note inside a folder', () => {
    assert.equal(rowExistsInTree('note', 11, folders, rootNotes, notesByFolder), true);
    assert.equal(rowExistsInTree('note', 21, folders, rootNotes, notesByFolder), true);
  });

  test('missing note', () => {
    assert.equal(rowExistsInTree('note', 777, folders, rootNotes, notesByFolder), false);
  });

  test('unknown kind', () => {
    assert.equal(rowExistsInTree('tag', 1, folders, rootNotes, notesByFolder), false);
  });

  test('bad id', () => {
    assert.equal(rowExistsInTree('note', 0, folders, rootNotes, notesByFolder), false);
    assert.equal(rowExistsInTree('note', NaN, folders, rootNotes, notesByFolder), false);
  });

  test('tolerates empty tree', () => {
    assert.equal(rowExistsInTree('folder', 1, [], [], {}), false);
    assert.equal(rowExistsInTree('note', 10, [], [], {}), false);
  });
});

describe('buildZipFilename', () => {
  test('uses the title and appends .zip', () => {
    assert.equal(buildZipFilename('note', 'My Note'), 'My Note.zip');
  });

  test('sanitises characters unsafe in filenames', () => {
    assert.equal(buildZipFilename('folder', 'a/b\\c:d*e?f"g<h>i|j'), 'a_b_c_d_e_f_g_h_i_j.zip');
  });

  test('truncates very long titles', () => {
    const long = 'x'.repeat(500);
    assert.ok(buildZipFilename('note', long).length <= 124); // 120 + '.zip'
  });

  test('falls back to kind label when title blank', () => {
    assert.equal(buildZipFilename('note', ''), 'note.zip');
    assert.equal(buildZipFilename('folder', '   '), 'folder.zip');
    assert.equal(buildZipFilename('note', null), 'note.zip');
  });
});
