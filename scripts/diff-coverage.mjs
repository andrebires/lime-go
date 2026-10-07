#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { execFileSync } from "node:child_process";
import { fileURLToPath, pathToFileURL } from "node:url";

export function parseChangedLines(diff) {
  const changed = new Map();
  let currentFile = null;
  for (const line of diff.split("\n")) {
    if (line.startsWith("+++ b/")) {
      currentFile = line.slice(6);
      if (!changed.has(currentFile)) changed.set(currentFile, new Set());
      continue;
    }
    const hunk = line.match(/^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@/);
    if (!hunk || !currentFile) continue;
    const start = Number(hunk[1]);
    const count = hunk[2] === undefined ? 1 : Number(hunk[2]);
    for (let lineNumber = start; lineNumber < start + count; lineNumber += 1)
      changed.get(currentFile).add(lineNumber);
  }
  return changed;
}

export function coverageResult(entries, threshold) {
  const executable = entries.filter((entry) => entry.executable);
  const covered = executable.filter((entry) => entry.covered);
  const percentage =
    executable.length === 0 ? 100 : (covered.length / executable.length) * 100;
  return {
    covered: covered.length,
    total: executable.length,
    percentage,
    passed: percentage + Number.EPSILON >= threshold,
  };
}

export function goEntries(profile, changedLines, repositoryRoot) {
  const blocksByFile = new Map();
  for (const line of profile.split("\n").slice(1)) {
    const match = line.match(/^(.+):(\d+)\.\d+,(\d+)\.\d+ \d+ (\d+)$/);
    if (!match) continue;
    const moduleRelative = match[1].replace(
      /^github\.com\/andrebires\/lime-go\/v2\//,
      "",
    );
    const file = path
      .relative(repositoryRoot, path.resolve(repositoryRoot, moduleRelative))
      .split(path.sep)
      .join("/");
    const blocks = blocksByFile.get(file) || [];
    blocks.push({
      start: Number(match[2]),
      end: Number(match[3]),
      covered: Number(match[4]) > 0,
    });
    blocksByFile.set(file, blocks);
  }
  const entries = [];
  for (const [file, lines] of changedLines) {
    if (!file.endsWith(".go") || file.endsWith("_test.go")) continue;
    const blocks = blocksByFile.get(file) || [];
    for (const line of lines) {
      const matching = blocks.filter(
        (block) => line >= block.start && line <= block.end,
      );
      entries.push({
        file,
        line,
        executable: matching.length > 0,
        covered: matching.some((block) => block.covered),
      });
    }
  }
  return entries;
}

export function javascriptEntries(
  coverageDirectory,
  changedLines,
  repositoryRoot,
) {
  const reports = fs
    .readdirSync(coverageDirectory)
    .filter((name) => name.endsWith(".json"));
  const scripts = new Map();
  for (const reportName of reports) {
    const report = JSON.parse(
      fs.readFileSync(path.join(coverageDirectory, reportName), "utf8"),
    );
    for (const script of report.result || []) {
      if (!script.url.startsWith("file:")) continue;
      const absolute = fileURLToPath(script.url.split("?")[0]);
      const relative = path
        .relative(repositoryRoot, absolute)
        .split(path.sep)
        .join("/");
      if (
        !relative.startsWith("examples/lime2-demo/") ||
        !relative.endsWith(".js")
      )
        continue;
      const existing = scripts.get(relative) || [];
      existing.push(...script.functions.flatMap((item) => item.ranges));
      scripts.set(relative, existing);
    }
  }

  const entries = [];
  for (const [file, lines] of changedLines) {
    if (!file.startsWith("examples/lime2-demo/") || !file.endsWith(".js"))
      continue;
    const absolute = path.join(repositoryRoot, file);
    if (!fs.existsSync(absolute)) continue;
    const source = fs.readFileSync(absolute, "utf8");
    const offsets = lineOffsets(source);
    const ranges = scripts.get(file) || [];
    for (const line of lines) {
      if (line < 1 || line > offsets.length) continue;
      const text = source.slice(
        offsets[line - 1],
        offsets[line] ?? source.length,
      );
      if (isDeclarativeLine(text)) continue;
      const firstCode = offsets[line - 1] + Math.max(0, text.search(/\S/));
      const matching = ranges.filter(
        (range) =>
          firstCode >= range.startOffset && firstCode < range.endOffset,
      );
      const mostSpecificLength = matching.reduce(
        (smallest, range) =>
          Math.min(smallest, range.endOffset - range.startOffset),
        Number.POSITIVE_INFINITY,
      );
      const mostSpecific = matching.filter(
        (range) => range.endOffset - range.startOffset === mostSpecificLength,
      );
      entries.push({
        file,
        line,
        executable: true,
        covered: mostSpecific.some((range) => range.count > 0),
      });
    }
  }
  return entries;
}

function lineOffsets(source) {
  const offsets = [0];
  for (let index = 0; index < source.length; index += 1) {
    if (source[index] === "\n") offsets.push(index + 1);
  }
  return offsets;
}

function isDeclarativeLine(line) {
  const trimmed = line.trim();
  return (
    trimmed === "" ||
    trimmed.startsWith("//") ||
    trimmed === "{" ||
    trimmed === "}" ||
    trimmed === "};"
  );
}

function main() {
  const args = parseArguments(process.argv.slice(2));
  const repositoryRoot = process.cwd();
  const diff = execFileSync(
    "git",
    ["diff", "--unified=0", "--no-color", args.base, "--", "*.go", "*.js"],
    { cwd: repositoryRoot, encoding: "utf8" },
  );
  const changedLines = parseChangedLines(diff);
  const untracked = execFileSync(
    "git",
    ["ls-files", "--others", "--exclude-standard", "--", "*.go", "*.js"],
    { cwd: repositoryRoot, encoding: "utf8" },
  );
  for (const file of untracked.split("\n").filter(Boolean)) {
    const source = fs.readFileSync(path.join(repositoryRoot, file), "utf8");
    const lines = changedLines.get(file) || new Set();
    for (let line = 1; line <= source.split("\n").length; line += 1)
      lines.add(line);
    changedLines.set(file, lines);
  }
  const goProfile = fs.readFileSync(args.goProfile, "utf8");
  const entries = [
    ...goEntries(goProfile, changedLines, repositoryRoot),
    ...javascriptEntries(args.v8Directory, changedLines, repositoryRoot),
  ];
  const result = coverageResult(entries, args.threshold);
  const files = [
    ...new Set(
      entries.filter((entry) => entry.executable).map((entry) => entry.file),
    ),
  ].sort();
  process.stdout.write(
    `Changed executable coverage: ${result.covered}/${result.total} (${result.percentage.toFixed(2)}%)\n`,
  );
  process.stdout.write(`Threshold: ${args.threshold.toFixed(2)}%\n`);
  process.stdout.write(
    `Changed files:\n${files.map((file) => `  - ${file}`).join("\n")}\n`,
  );
  if (!result.passed) {
    const uncovered = entries
      .filter((entry) => entry.executable && !entry.covered)
      .map((entry) => `${entry.file}:${entry.line}`);
    process.stderr.write(
      `Uncovered changed lines:\n${uncovered.map((line) => `  - ${line}`).join("\n")}\n`,
    );
    process.exitCode = 1;
  }
}

function parseArguments(args) {
  const values = {
    base: "origin/master",
    threshold: 90,
    goProfile: "",
    v8Directory: "",
  };
  for (let index = 0; index < args.length; index += 2) {
    const key = args[index];
    const value = args[index + 1];
    if (key === "--base") values.base = value;
    else if (key === "--threshold") values.threshold = Number(value);
    else if (key === "--go-profile") values.goProfile = value;
    else if (key === "--v8-directory") values.v8Directory = value;
    else throw new Error(`unknown argument ${key}`);
  }
  if (
    !values.goProfile ||
    !values.v8Directory ||
    !Number.isFinite(values.threshold)
  )
    throw new Error(
      "--go-profile, --v8-directory, and a numeric --threshold are required",
    );
  return values;
}

const invokedPath = process.argv[1]
  ? pathToFileURL(path.resolve(process.argv[1])).href
  : "";
if (import.meta.url === invokedPath) main();
