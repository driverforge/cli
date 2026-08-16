// SARIF handling for the qlty maintainability gate.
//
// Shared with our TypeScript repo, where the same three scripts replaced the
// Qlty Cloud GitHub App. Keep the two in step when either moves — the logic
// here is language-agnostic (it only reads qlty's SARIF), so the Go and
// TypeScript repos share it verbatim apart from paths.
//
// The gate reports every smell qlty finds in a file the branch changed —
// including ones the branch did not write. That is deliberate and it is what
// the Qlty Cloud App did: touching a file makes its debt yours. A gate scoped
// to newly-written lines only would let inherited debt sit untouched forever.
//
// Two things still have to be worked out, and this module does both.
//
// Scope. `qlty smells --upstream <ref>` narrows to changed files, but a path-
// scoped run only detects duplication *within* the paths it was given, so a new
// copy of a block that lives in an untouched file goes unseen. The gate
// therefore always scans the whole repo and filters the results here.
//
// Attribution. Failing is not the same as explaining. Each finding is labelled
// against a second scan of the merge base, so the report can separate what the
// branch introduced from what it inherited by opening the file. Both fail the
// build; only the wording differs. Labelling is not load-bearing — a mislabelled
// finding is cosmetic, which is why the baseline scan does not need repeating.

const DUPLICATION_RULES = new Set(['qlty:identical-code', 'qlty:similar-code']);

// "(count = 28)", "(level = 5)", "(mass = 117)" — the severity of a finding,
// which moves as code is edited. Stripped from the fingerprint so that editing
// an already-flagged function still matches it in the baseline.
const METRIC_PATTERN = /\((?:count|level|mass) = ([\d.]+)\)/;

function ruleName(ruleId) {
  return String(ruleId || 'qlty:unknown').replace(/^qlty:/, '');
}

function primaryLocation(result) {
  const physical = result?.locations?.[0]?.physicalLocation;
  return {
    file: physical?.artifactLocation?.uri || '<unknown>',
    startLine: physical?.region?.startLine ?? 1,
    endLine: physical?.region?.endLine ?? physical?.region?.startLine ?? 1,
  };
}

function relatedLocations(result) {
  return (result?.relatedLocations || []).map((location) => {
    const physical = location?.physicalLocation;
    return {
      file: physical?.artifactLocation?.uri || '<unknown>',
      startLine: physical?.region?.startLine ?? 1,
    };
  });
}

function metricOf(message) {
  const match = METRIC_PATTERN.exec(message || '');
  return match ? Number(match[1]) : null;
}

// Duplication carries `structural_hash`, a content hash of the duplicated node,
// which survives the block moving within the file. Nothing else does: only
// function-complexity has a `function.name` fingerprint, and boolean-logic and
// nested-control-flow have none at all. For those the normalised message is the
// best available identity — coarse, but it is per file and per rule, and the
// ordinal assigned in parseSarif separates repeats.
function fingerprint(result) {
  const { file } = primaryLocation(result);
  const ruleId = result?.ruleId || 'qlty:unknown';
  if (DUPLICATION_RULES.has(ruleId)) {
    const hash = result?.properties?.structural_hash;
    if (hash) return `${ruleId}|${file}|${hash}`;
  }
  const message = String(result?.message?.text || '').replace(METRIC_PATTERN, '').trim();
  return `${ruleId}|${file}|${message}`;
}

// Sorted before ordinals are assigned so that "the third boolean-logic finding
// in this file" means the same thing on both sides of the comparison.
function parseSarif(sarif) {
  const results = sarif?.runs?.[0]?.results || [];
  const findings = results.map((result) => {
    const location = primaryLocation(result);
    const message = String(result?.message?.text || '').trim();
    return {
      ruleId: result?.ruleId || 'qlty:unknown',
      rule: ruleName(result?.ruleId),
      message,
      metric: metricOf(message),
      file: location.file,
      startLine: location.startLine,
      endLine: location.endLine,
      related: relatedLocations(result),
      fingerprint: fingerprint(result),
    };
  });
  findings.sort(
    (a, b) =>
      a.file.localeCompare(b.file) ||
      a.startLine - b.startLine ||
      a.message.localeCompare(b.message),
  );
  return findings;
}

function groupByFingerprint(findings) {
  const groups = new Map();
  for (const finding of findings) {
    const existing = groups.get(finding.fingerprint);
    if (existing) existing.push(finding);
    else groups.set(finding.fingerprint, [finding]);
  }
  return groups;
}

// The branch answers for the files it touched. A duplication finding counts as
// touching a file if either half of the pair is in the diff, so adding a copy of
// an untouched block is caught at both ends.
function touchesChangedFiles(finding, changed) {
  return changed.has(finding.file) || finding.related.some((r) => changed.has(r.file));
}

// Everything qlty found in the changed files, split by whether the merge base
// already had it. Occurrences beyond the baseline's count for a fingerprint are
// the branch's; which specific occurrence gets the label is arbitrary when a
// fingerprint repeats within a file, but the split is exact.
function classifyFindings(baseFindings, headFindings, changedFiles) {
  const changed = new Set(changedFiles || []);
  const baseCounts = new Map(
    [...groupByFingerprint(baseFindings)].map(([key, group]) => [key, group.length]),
  );
  const baseMetrics = new Map(
    [...groupByFingerprint(baseFindings)].map(([key, group]) => [
      key,
      Math.max(...group.map((f) => f.metric ?? 0)),
    ]),
  );

  const introduced = [];
  const inherited = [];
  for (const [key, group] of groupByFingerprint(headFindings)) {
    const baseCount = baseCounts.get(key) ?? 0;
    group.forEach((finding, ordinal) => {
      if (changedFiles && !touchesChangedFiles(finding, changed)) return;
      if (ordinal >= baseCount) {
        introduced.push(finding);
        return;
      }
      const baseMetric = baseMetrics.get(key) ?? 0;
      const worsened = finding.metric !== null && baseMetric > 0 && finding.metric > baseMetric;
      inherited.push(worsened ? { ...finding, baseMetric } : finding);
    });
  }

  const byLocation = (a, b) => a.file.localeCompare(b.file) || a.startLine - b.startLine;
  introduced.sort(byLocation);
  inherited.sort(byLocation);
  return { introduced, inherited };
}

function totalFindings(diff) {
  return diff.introduced.length + diff.inherited.length;
}

function describe(finding) {
  const where = finding.related.length
    ? ` (also at ${finding.related.map((r) => `${r.file}:${r.startLine}`).join(', ')})`
    : '';
  const worse = finding.baseMetric !== undefined ? ` — was ${finding.baseMetric} before this branch` : '';
  return `${finding.message}${where}${worse}`;
}

// GitHub renders these against the diff when the file is part of it, and
// collects them on the job summary either way. The message is the payload the
// build log is read for, so it carries the related locations too.
function annotations(diff) {
  return [
    ...diff.introduced.map((f) => annotation(f, `qlty: ${f.rule}`, describe(f))),
    ...diff.inherited.map((f) =>
      annotation(f, `qlty: ${f.rule} (pre-existing, in a file you changed)`, describe(f)),
    ),
  ];
}

function annotation(finding, title, message) {
  const escape = (value) =>
    String(value).replace(/%/g, '%25').replace(/\r/g, '%0D').replace(/\n/g, '%0A');
  const properties = [
    `file=${finding.file}`,
    `line=${finding.startLine}`,
    `endLine=${Math.max(finding.endLine, finding.startLine)}`,
    `title=${escape(title)}`,
  ].join(',');
  return `::error ${properties}::${escape(message)}`;
}

function plainReport(diff) {
  const lines = [];
  const section = (heading, findings) => {
    if (!findings.length) return;
    lines.push('', heading, '');
    for (const finding of findings) {
      lines.push(`  ${finding.file}:${finding.startLine}  [${finding.rule}]`);
      lines.push(`    ${describe(finding)}`);
    }
  };
  section('Code smells introduced by this branch:', diff.introduced);
  section(
    'Pre-existing code smells in files this branch changed (boy scout: clean these up):',
    diff.inherited,
  );
  const total = totalFindings(diff);
  lines.push(
    '',
    total === 0
      ? 'qlty: clean.'
      : `qlty: ${total} finding(s) — ${diff.introduced.length} introduced, ${diff.inherited.length} pre-existing.`,
  );
  return lines.join('\n');
}

function markdownReport(diff, { headSha, baseSha } = {}) {
  const total = totalFindings(diff);
  const lines = ['### 🧹 Qlty maintainability', ''];
  if (total === 0) {
    lines.push('No code smells in the files this branch changed. ✅');
  } else {
    lines.push(
      `**${total} finding(s)** in files this branch changed — ${diff.introduced.length} introduced, ${diff.inherited.length} pre-existing.`,
      '',
      'Pre-existing smells count: editing a file makes its debt yours to clean up.',
      '',
      '| | Rule | Location | Detail |',
      '|---|---|---|---|',
    );
    for (const finding of diff.introduced) {
      lines.push(
        `| 🆕 | \`${finding.rule}\` | \`${finding.file}:${finding.startLine}\` | ${describe(finding)} |`,
      );
    }
    for (const finding of diff.inherited) {
      lines.push(
        `| 🧹 | \`${finding.rule}\` | \`${finding.file}:${finding.startLine}\` | ${describe(finding)} |`,
      );
    }
    lines.push('', 'Thresholds live in `.qlty/qlty.toml`. Reproduce locally with `make qlty`.');
  }
  if (headSha && baseSha) {
    lines.push(
      '',
      `<sub>\`qlty smells --all\` on ${headSha.slice(0, 7)}, scoped to files changed since ${baseSha.slice(0, 7)}.</sub>`,
    );
  }
  return lines.join('\n');
}

module.exports = {
  DUPLICATION_RULES,
  annotations,
  classifyFindings,
  describe,
  fingerprint,
  markdownReport,
  parseSarif,
  plainReport,
  totalFindings,
  touchesChangedFiles,
};
