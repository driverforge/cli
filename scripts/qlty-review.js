#!/usr/bin/env node
// Inline PR review comments for the qlty gate. Shared verbatim with our
// TypeScript repo's copy — keep the two in step.
//
//   node scripts/qlty-review.js reports/qlty/findings.json
//
// The gate itself is scripts/qlty-gate.js; this only mirrors its findings onto the
// diff, the way the Qlty Cloud App used to. It is best-effort by design — the
// workflow runs it with continue-on-error, because a red X for "could not post a
// comment" on an otherwise clean branch is exactly the kind of unreadable
// failure this ticket set out to remove.
//
// GitHub rejects a review comment whose line is not part of the diff, so a
// finding is only posted if some line it spans appears in a hunk. A finding
// covering an entire pre-existing function that the branch changed one line of
// therefore lands on that one line; the rest are in the step log and the summary
// comment, which are complete.

const { execFileSync } = require('child_process');
const { readFileSync } = require('fs');

const MARKER = '<!-- driverforge-qlty-inline -->';

function gh(args) {
  return execFileSync('gh', args, { encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 });
}

function api(path, options = []) {
  return JSON.parse(gh(['api', path, ...options]));
}

// Right-hand line numbers reachable in a unified diff: added lines and the
// context around them. GitHub accepts a comment on any of these and 422s on
// anything else.
function commentableLines(patch) {
  const lines = new Set();
  if (!patch) return lines;
  let cursor = 0;
  for (const line of patch.split('\n')) {
    const header = /^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(line);
    if (header) {
      cursor = Number(header[1]);
      continue;
    }
    if (line.startsWith('-')) continue;
    if (line.startsWith('+') || line.startsWith(' ')) {
      lines.add(cursor);
      cursor++;
    }
  }
  return lines;
}

// Anchor to the finding's own line when the diff reaches it, else the first line
// of the finding's span that it does reach.
function anchorLine(finding, commentable) {
  if (commentable.has(finding.startLine)) return finding.startLine;
  const end = Math.max(finding.endLine ?? finding.startLine, finding.startLine);
  for (let line = finding.startLine; line <= end; line++) {
    if (commentable.has(line)) return line;
  }
  return null;
}

function body(finding) {
  const related = (finding.related || [])
    .map((r) => `\n\nAlso at \`${r.file}:${r.startLine}\`.`)
    .join('');
  const heading =
    finding.baseMetric !== undefined
      ? `**qlty: \`${finding.rule}\`** — pre-existing, and worse on this branch (was ${finding.baseMetric})`
      : `**qlty: \`${finding.rule}\`**`;
  return `${MARKER}\n${heading}\n\n${finding.message}${related}\n\n<sub>Threshold in \`.qlty/qlty.toml\`. Reproduce with \`make qlty\`.</sub>`;
}

function buildComments(findings, patches, alreadyPosted) {
  const comments = [];
  for (const finding of findings) {
    const commentable = commentableLines(patches.get(finding.file));
    const line = anchorLine(finding, commentable);
    if (line === null) continue;
    const key = `${finding.file}:${line}:${finding.rule}`;
    if (alreadyPosted.has(key)) continue;
    alreadyPosted.add(key);
    comments.push({ path: finding.file, line, side: 'RIGHT', body: body(finding) });
  }
  return comments;
}

// GitLab has no batch review: each finding is its own discussion thread, anchored
// by the merge request's diff refs. Same rules otherwise — only lines the diff
// reaches, and nothing already posted.
async function gitlabReview(findings) {
  const base = process.env.GITLAB_API_URL || 'https://gitlab.com/api/v4';
  const project = encodeURIComponent(process.env.GITLAB_PROJECT);
  const mr = process.env.PR_NUMBER;
  const headers = { 'PRIVATE-TOKEN': process.env.GITLAB_TOKEN, 'Content-Type': 'application/json' };
  const root = `${base}/projects/${project}/merge_requests/${mr}`;

  async function call(url, init) {
    const res = await fetch(url, { headers, ...init });
    if (!res.ok) throw new Error(`${init?.method || 'GET'} ${url}: ${res.status}`);
    return res;
  }

  async function pages(path) {
    const out = [];
    for (let page = 1; ; page++) {
      const res = await call(`${root}${path}${path.includes('?') ? '&' : '?'}per_page=100&page=${page}`);
      const items = await res.json();
      out.push(...items);
      if (items.length < 100) return out;
    }
  }

  const refs = (await (await call(root)).json()).diff_refs;
  if (!refs) throw new Error('merge request has no diff refs yet');

  const diffs = await pages('/diffs');
  const patches = new Map(diffs.map((file) => [file.new_path, file.diff]));

  const alreadyPosted = new Set();
  for (const discussion of await pages('/discussions')) {
    for (const note of discussion.notes) {
      if (!String(note.body || '').includes(MARKER) || !note.position) continue;
      const rule = /qlty: `([^`]+)`/.exec(note.body)?.[1] ?? '';
      alreadyPosted.add(`${note.position.new_path}:${note.position.new_line}:${rule}`);
    }
  }

  const comments = buildComments(findings, patches, alreadyPosted);
  if (comments.length === 0) {
    console.log('qlty: nothing new to comment on the diff.');
    return;
  }
  const oldPaths = new Map(diffs.map((file) => [file.new_path, file.old_path]));
  for (const comment of comments) {
    await call(`${root}/discussions`, {
      method: 'POST',
      body: JSON.stringify({
        body: comment.body,
        position: {
          position_type: 'text',
          base_sha: refs.base_sha,
          start_sha: refs.start_sha,
          head_sha: refs.head_sha,
          new_path: comment.path,
          old_path: oldPaths.get(comment.path) || comment.path,
          new_line: comment.line,
        },
      }),
    });
  }
  console.log(`qlty: posted ${comments.length} inline comment(s).`);
}

function githubReview(findings) {
  const repo = process.env.GITHUB_REPOSITORY;
  const pr = process.env.PR_NUMBER;
  if (!repo || !pr) throw new Error('GITHUB_REPOSITORY and PR_NUMBER must be set');

  const files = api(`repos/${repo}/pulls/${pr}/files`, ['--paginate', '--slurp']).flat();
  const patches = new Map(files.map((file) => [file.filename, file.patch]));

  // Re-runs on a pushed commit must not stack duplicate comments on lines that
  // survived the push.
  const existing = api(`repos/${repo}/pulls/${pr}/comments`, ['--paginate', '--slurp']).flat();
  const alreadyPosted = new Set(
    existing
      .filter((comment) => String(comment.body || '').includes(MARKER))
      .map((comment) => {
        const rule = /qlty: `([^`]+)`/.exec(comment.body)?.[1] ?? '';
        return `${comment.path}:${comment.line}:${rule}`;
      }),
  );

  const comments = buildComments(findings, patches, alreadyPosted);
  if (comments.length === 0) {
    console.log('qlty: nothing new to comment on the diff.');
    return;
  }

  // A single review rather than N standalone comments: one notification, and the
  // findings stay grouped in the Files tab. COMMENT, not REQUEST_CHANGES — the
  // build status is the gate, and a review that blocks merge would need a human
  // to dismiss it after the fix.
  const payload = {
    event: 'COMMENT',
    ...(process.env.HEAD_SHA ? { commit_id: process.env.HEAD_SHA } : {}),
    comments,
  };
  execFileSync('gh', ['api', '--method', 'POST', `repos/${repo}/pulls/${pr}/reviews`, '--input', '-'], {
    input: JSON.stringify(payload),
    encoding: 'utf8',
    stdio: ['pipe', 'ignore', 'inherit'],
  });
  console.log(`qlty: posted ${comments.length} inline comment(s).`);
}

// Posts to a GitLab merge request when GITLAB_PROJECT is set, else to a GitHub
// pull request through `gh`.
async function main() {
  const [findingsPath] = process.argv.slice(2);
  if (!findingsPath) throw new Error('Usage: node scripts/qlty-review.js <findings.json>');

  const { introduced = [], inherited = [] } = JSON.parse(readFileSync(findingsPath, 'utf8'));
  const findings = [...introduced, ...inherited];
  if (findings.length === 0) return 0;

  if (process.env.GITLAB_PROJECT) {
    if (!process.env.GITLAB_TOKEN || !process.env.PR_NUMBER) {
      throw new Error('GITLAB_TOKEN and PR_NUMBER must be set');
    }
    await gitlabReview(findings);
  } else {
    githubReview(findings);
  }
  return 0;
}

module.exports = { anchorLine, body, buildComments, commentableLines, MARKER };

if (require.main === module) {
  main().then(
    (code) => process.exit(code),
    (err) => {
      console.error(`qlty review comments skipped: ${err.message}`);
      process.exit(1);
    },
  );
}
