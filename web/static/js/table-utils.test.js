'use strict';
const { describe, test } = require('node:test');
const assert = require('node:assert/strict');
const { parseCSVLine, parseTabularText, rowsToMarkdownTable } = require('./table-utils.js');

// ── parseCSVLine ──────────────────────────────────────────────────────────────

describe('parseCSVLine', () => {
  test('simple values', () => {
    assert.deepEqual(parseCSVLine('a,b,c'), ['a', 'b', 'c']);
  });

  test('quoted field', () => {
    assert.deepEqual(parseCSVLine('"hello world",b'), ['hello world', 'b']);
  });

  test('quoted field with embedded comma', () => {
    assert.deepEqual(parseCSVLine('"a,b",c'), ['a,b', 'c']);
  });

  test('doubled quote inside quoted field is unescaped', () => {
    assert.deepEqual(parseCSVLine('"say ""hi""",b'), ['say "hi"', 'b']);
  });

  test('empty fields', () => {
    assert.deepEqual(parseCSVLine(',b,'), ['', 'b', '']);
  });

  test('single field with no comma', () => {
    assert.deepEqual(parseCSVLine('alone'), ['alone']);
  });

  test('all empty fields', () => {
    assert.deepEqual(parseCSVLine(',,'), ['', '', '']);
  });
});

// ── parseTabularText ──────────────────────────────────────────────────────────

describe('parseTabularText', () => {
  test('TSV header + two data rows', () => {
    const result = parseTabularText('Name\tAge\nAlice\t30\nBob\t25');
    assert.deepEqual(result, [['Name', 'Age'], ['Alice', '30'], ['Bob', '25']]);
  });

  test('TSV three columns', () => {
    const result = parseTabularText('A\tB\tC\n1\t2\t3');
    assert.deepEqual(result, [['A', 'B', 'C'], ['1', '2', '3']]);
  });

  test('TSV header row only (one row)', () => {
    const result = parseTabularText('Name\tAge');
    assert.deepEqual(result, [['Name', 'Age']]);
  });

  test('CSV header + data rows', () => {
    const result = parseTabularText('Name,Age\nAlice,30\nBob,25');
    assert.deepEqual(result, [['Name', 'Age'], ['Alice', '30'], ['Bob', '25']]);
  });

  test('CSV header row only', () => {
    const result = parseTabularText('Name,Age');
    assert.deepEqual(result, [['Name', 'Age']]);
  });

  test('CRLF line endings (Windows / Excel)', () => {
    const result = parseTabularText('Name\tAge\r\nAlice\t30');
    assert.deepEqual(result, [['Name', 'Age'], ['Alice', '30']]);
  });

  test('ragged TSV rows are padded to max width', () => {
    const result = parseTabularText('A\tB\tC\n1\t2');
    assert.equal(result !== null, true);
    assert.equal(result[1].length, 3);
    assert.equal(result[1][2], '');
  });

  test('TSV preferred over CSV when both delimiters present', () => {
    // Tab + comma in same text: TSV should win (Excel first)
    const result = parseTabularText('A,B\tC\n1,2\t3');
    assert.ok(result !== null);
    assert.equal(result[0][0], 'A,B'); // split on tab, so first cell is "A,B"
  });

  test('single-column TSV returns null', () => {
    assert.equal(parseTabularText('line1\nline2'), null);
  });

  test('plain prose returns null', () => {
    assert.equal(parseTabularText('This is a sentence.\nAnd another sentence.'), null);
  });

  test('empty string returns null', () => {
    assert.equal(parseTabularText(''), null);
  });

  test('null returns null', () => {
    assert.equal(parseTabularText(null), null);
  });

  test('only whitespace lines returns null', () => {
    assert.equal(parseTabularText('   \n   \n'), null);
  });

  test('cells are trimmed', () => {
    const result = parseTabularText(' Name \t Age \n Alice \t 30 ');
    assert.deepEqual(result, [['Name', 'Age'], ['Alice', '30']]);
  });
});

// ── rowsToMarkdownTable ───────────────────────────────────────────────────────

describe('rowsToMarkdownTable', () => {
  test('header + two data rows', () => {
    const rows = [['Name', 'Age'], ['Alice', '30'], ['Bob', '25']];
    const lines = rowsToMarkdownTable(rows).split('\n');
    assert.equal(lines[0], '| Name | Age |');
    assert.equal(lines[1], '| --- | --- |');
    assert.equal(lines[2], '| Alice | 30 |');
    assert.equal(lines[3], '| Bob | 25 |');
  });

  test('header only produces two lines (header + separator)', () => {
    const lines = rowsToMarkdownTable([['Col A', 'Col B']]).split('\n');
    assert.equal(lines.length, 2);
    assert.equal(lines[0], '| Col A | Col B |');
    assert.equal(lines[1], '| --- | --- |');
  });

  test('three-column separator has three dashes blocks', () => {
    const result = rowsToMarkdownTable([['A', 'B', 'C'], ['1', '2', '3']]);
    assert.ok(result.includes('| --- | --- | --- |'));
  });

  test('pipe character in cell is escaped', () => {
    const rows = [['a|b', 'c'], ['d', 'e']];
    const result = rowsToMarkdownTable(rows);
    assert.ok(result.includes('a\\|b'));
  });

  test('embedded newline in cell collapses to space', () => {
    const rows = [['a\nb', 'c'], ['d', 'e']];
    const result = rowsToMarkdownTable(rows);
    assert.ok(result.includes('a b'));
    assert.ok(!result.includes('a\nb'));
  });

  test('null cell becomes empty string', () => {
    const rows = [['A', 'B'], [null, 'x']];
    const result = rowsToMarkdownTable(rows);
    assert.ok(result.includes('|  | x |'));
  });

  test('empty rows array returns empty string', () => {
    assert.equal(rowsToMarkdownTable([]), '');
  });

  test('null input returns empty string', () => {
    assert.equal(rowsToMarkdownTable(null), '');
  });

  test('round-trip: TSV paste → parse → table', () => {
    const tsv = 'Product\tPrice\tQty\nApple\t1.20\t100\nBanana\t0.50\t200';
    const rows = parseTabularText(tsv);
    const table = rowsToMarkdownTable(rows);
    const lines = table.split('\n');
    assert.equal(lines[0], '| Product | Price | Qty |');
    assert.equal(lines[1], '| --- | --- | --- |');
    assert.equal(lines[2], '| Apple | 1.20 | 100 |');
    assert.equal(lines[3], '| Banana | 0.50 | 200 |');
  });

  test('round-trip: CSV paste → parse → table', () => {
    const csv = 'City,Country\n"New York","USA"\nParis,France';
    const rows = parseTabularText(csv);
    const table = rowsToMarkdownTable(rows);
    const lines = table.split('\n');
    assert.equal(lines[0], '| City | Country |');
    assert.equal(lines[2], '| New York | USA |');
    assert.equal(lines[3], '| Paris | France |');
  });
});
