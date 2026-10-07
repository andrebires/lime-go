import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import {
  parseChangedLines,
  coverageResult,
  goEntries,
  javascriptEntries,
} from "./diff-coverage.mjs";
test("diff hunks include new executable lines and exclude deleted lines", () => {
  const changed = parseChangedLines(
    "+++ b/example.go\n@@ -1,2 +1,3 @@\n+++ b/removed.go\n@@ -1,3 +0,0 @@\n+++ b/new.js\n@@ -0,0 +1 @@",
  );
  assert.deepEqual([...changed.get("example.go")], [1, 2, 3]);
  assert.equal(changed.get("removed.go").size, 0);
  assert.deepEqual([...changed.get("new.js")], [1]);
});
test("coverage percentages count executable changed lines only", () => {
  assert.equal(coverageResult([], 90).percentage, 100);
  assert.deepEqual(
    coverageResult(
      [
        { executable: true, covered: true },
        { executable: true, covered: false },
        { executable: false, covered: false },
      ],
      90,
    ),
    { covered: 1, total: 2, percentage: 50, passed: false },
  );
});
test("Go coverage maps module paths, excludes test files and checks missing branches", () => {
  const changed = new Map([
    ["envelope.go", new Set([1, 2, 3, 4, 5])],
    ["envelope_test.go", new Set([2])],
  ]);
  const profile =
    "mode: atomic\ngithub.com/andrebires/lime-go/v2/envelope.go:2.1,3.2 1 1\ngithub.com/andrebires/lime-go/v2/envelope.go:4.1,4.2 1 0\n";
  const result = coverageResult(goEntries(profile, changed, process.cwd()), 90);
  assert.equal(result.total, 3);
  assert.equal(result.covered, 2);
  assert.equal(result.passed, false);
});
test("V8 coverage uses most specific ranges and merges independent reports", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "lime-coverage-"));
  try {
    const file = "examples/lime2-demo/client.js",
      source = fs.readFileSync(file, "utf8"),
      offset = source.indexOf("const $");
    const line = source.slice(0, offset).split("\n").length;
    const missing = javascriptEntries(
      dir,
      new Map([[file, new Set([line])]]),
      process.cwd(),
    );
    assert.equal(missing[0].executable, true);
    assert.equal(missing[0].covered, false);
    fs.writeFileSync(
      path.join(dir, "coverage.json"),
      JSON.stringify({
        result: [
          {
            url: pathToFileURL(path.join(process.cwd(), file)).href,
            functions: [
              {
                ranges: [
                  { startOffset: 0, endOffset: source.length, count: 1 },
                  {
                    startOffset: offset,
                    endOffset: offset + source.slice(offset).indexOf("\n"),
                    count: 0,
                  },
                ],
              },
            ],
          },
        ],
      }),
    );
    const entries = javascriptEntries(
      dir,
      new Map([[file, new Set([line])]]),
      process.cwd(),
    );
    assert.equal(entries.length, 1);
    assert.equal(entries[0].covered, false);
    fs.writeFileSync(
      path.join(dir, "coverage2.json"),
      JSON.stringify({
        result: [
          {
            url: pathToFileURL(path.join(process.cwd(), file)).href,
            functions: [
              {
                ranges: [
                  {
                    startOffset: offset,
                    endOffset: offset + source.slice(offset).indexOf("\n"),
                    count: 1,
                  },
                ],
              },
            ],
          },
        ],
      }),
    );
    assert.equal(
      javascriptEntries(
        dir,
        new Map([[file, new Set([line])]]),
        process.cwd(),
      )[0].covered,
      true,
    );
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
