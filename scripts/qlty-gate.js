#!/usr/bin/env node
// The qlty maintainability gate. Shared verbatim with our TypeScript repo's
// copy — keep the two in step. `make qlty` runs this locally.
//
//   node scripts/qlty-gate.js [--base <ref>] [--out-dir <dir>] [--all-files] [--no-baseline]
//
// Exits non-zero if `qlty smells` reports anything in a file the branch changed,
// whether or not the branch wrote it — see the header of qlty-diff.js for why
// inherited debt counts. Findings go to stdout, and under GitHub Actions also to
// the annotation stream and the job summary.
//
// This replaces the Qlty Cloud GitHub App, which gated the same thresholds but
// kept the failure list behind a login on qlty.sh — a red check with nothing
// readable attached to it. Everything here comes from the open-source CLI, so
// the same command reproduces the gate locally.
//
// The scan is whole-repo, because duplication is only detected within the set of
// files analysed and a changed-files-only scan would miss a new copy of an
// untouched block. A second scan of the merge base labels each finding as
// introduced or inherited; `--no-baseline` skips it and reports everything
// unlabelled, which halves the runtime and changes no verdict.

const { execFileSync, spawnSync } = require('child_process');
const { cpSync, mkdirSync, mkdtempSync, rmSync, writeFileSync, appendFileSync } = require('fs');
const os = require('os');
const path = require('path');

const {
  annotations,
  classifyFindings,
  markdownReport,
  parseSarif,
  plainReport,
  totalFindings,
} = require('./qlty-diff');

const REPO_ROOT = path.join(__dirname, '..');

function parseArgs(argv) {
  const args = {
    base: process.env.QLTY_BASE_REF || 'origin/main',
    outDir: null,
    allFiles: false,
    baseline: true,
  };
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--base') args.base = argv[++i];
    else if (argv[i] === '--out-dir') args.outDir = argv[++i];
    else if (argv[i] === '--all-files') args.allFiles = true;
    else if (argv[i] === '--no-baseline') args.baseline = false;
    else if (argv[i] === '--help' || argv[i] === '-h') args.help = true;
    else throw new Error(`Unknown argument: ${argv[i]}`);
  }
  return args;
}

function git(args, cwd = REPO_ROOT) {
  return execFileSync('git', args, { cwd, encoding: 'utf8' }).trim();
}

// A PR's base branch moves while the PR is open, so comparing against its tip
// would scope the gate to files other people changed too. The merge base is the
// commit the branch actually diverged from.
function resolveBase(ref) {
  try {
    return git(['merge-base', 'HEAD', ref]);
  } catch {
    throw new Error(
      `Cannot resolve a merge base with "${ref}". Fetch it first (CI needs fetch-depth: 0).`,
    );
  }
}

// The version CI pins in .buildkite/steps/run-qlty.sh. Older CLIs are rejected up
// front: `qlty smells --sarif` is not accepted by older builds (0.511.0, the
// current Homebrew default, rejects the flag), and the raw failure is a clap
// usage dump that reads like a bug in the gate rather than "upgrade qlty".
// Requiring the pinned version also keeps a local `make qlty` reporting what CI
// will report, which is the point of having a local command at all.
//
// (Local addition, not in the TypeScript copy — see the header.)
const MIN_QLTY_VERSION = '0.640.0';

function parseVersion(text) {
  const match = /(\d+)\.(\d+)\.(\d+)/.exec(text || '');
  return match ? [Number(match[1]), Number(match[2]), Number(match[3])] : null;
}

function olderThanMin(version) {
  const found = parseVersion(version);
  const min = parseVersion(MIN_QLTY_VERSION);
  if (!found) return false; // unparseable — let the run proceed and fail loudly
  for (let i = 0; i < 3; i++) {
    if (found[i] !== min[i]) return found[i] < min[i];
  }
  return false;
}

function qltyBin() {
  const bin = process.env.QLTY_BIN || 'qlty';
  const probe = spawnSync(bin, ['--version'], { encoding: 'utf8' });
  if (probe.status !== 0) {
    throw new Error(
      `qlty CLI not found (tried "${bin}"). Download v${MIN_QLTY_VERSION} from ` +
        `https://github.com/qltysh/qlty/releases/tag/v${MIN_QLTY_VERSION} and set QLTY_BIN ` +
        'to it — the Homebrew tap lags too far behind to satisfy this gate.',
    );
  }
  const version = probe.stdout.trim();
  if (olderThanMin(version)) {
    throw new Error(
      `qlty ${version.split(' ')[1] || version} is too old — this gate needs ${MIN_QLTY_VERSION}, ` +
        'the version CI pins, for `smells --sarif`. ' +
        // The Homebrew tap lags well behind upstream's releases, so `brew upgrade`
        // reports "already installed" and leaves this gate failing. CI downloads
        // the pinned build directly; do the same and point QLTY_BIN at it.
        `Download v${MIN_QLTY_VERSION} from ` +
        `https://github.com/qltysh/qlty/releases/tag/v${MIN_QLTY_VERSION} ` +
        'and set QLTY_BIN to it.',
    );
  }
  return { bin, version };
}

function runSmells(bin, cwd) {
  const result = spawnSync(bin, ['smells', '--all', '--quiet', '--sarif', '--no-upgrade-check'], {
    cwd,
    encoding: 'utf8',
    maxBuffer: 256 * 1024 * 1024,
  });
  if (result.error) throw result.error;
  // `qlty smells` exits 0 whether or not it found anything, so a non-zero status
  // is a real failure (bad config, unreadable tree) and must not be read as a
  // clean run — that would turn the gate green by accident.
  if (result.status !== 0) {
    throw new Error(`qlty smells failed in ${cwd} (exit ${result.status}):\n${result.stderr}`);
  }
  try {
    return JSON.parse(result.stdout);
  } catch (err) {
    throw new Error(`qlty smells produced unparseable SARIF in ${cwd}: ${err.message}`);
  }
}

// The baseline scan uses the *head* copy of `.qlty/qlty.toml`, so a branch that
// moves a threshold sees its own thresholds on both sides and the labels stay
// about code rather than config.
function withBaseWorktree(baseSha, fn) {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'qlty-base-'));
  const worktree = path.join(dir, 'base');
  git(['worktree', 'add', '--detach', '--quiet', worktree, baseSha]);
  try {
    cpSync(path.join(REPO_ROOT, '.qlty'), path.join(worktree, '.qlty'), { recursive: true });
    return fn(worktree);
  } finally {
    try {
      git(['worktree', 'remove', '--force', worktree]);
    } catch {
      rmSync(worktree, { recursive: true, force: true });
    }
    rmSync(dir, { recursive: true, force: true });
  }
}

function emit(diff, { headSha, baseSha }) {
  console.log(plainReport(diff));

  if (!process.env.GITHUB_ACTIONS) return;
  for (const line of annotations(diff)) console.log(line);
  if (process.env.GITHUB_STEP_SUMMARY) {
    appendFileSync(
      process.env.GITHUB_STEP_SUMMARY,
      `${markdownReport(diff, { headSha, baseSha })}\n`,
    );
  }
}

function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.help) {
    console.log(
      'Usage: node scripts/qlty-gate.js [--base <ref>] [--out-dir <dir>] [--all-files] [--no-baseline]',
    );
    return 0;
  }

  const { bin, version } = qltyBin();
  const baseSha = resolveBase(args.base);
  const headSha = git(['rev-parse', 'HEAD']);
  console.log(`${version}\nhead ${headSha}\nbase ${baseSha} (merge-base with ${args.base})`);

  const changedFiles = git(['diff', '--name-only', `${baseSha}..${headSha}`])
    .split('\n')
    .filter(Boolean);
  console.log(`${changedFiles.length} changed file(s)\n`);

  if (changedFiles.length === 0 && !args.allFiles) {
    console.log('qlty: no changed files — nothing to check.');
    return 0;
  }

  const headSarif = runSmells(bin, REPO_ROOT);
  const baseSarif = args.baseline
    ? withBaseWorktree(baseSha, (worktree) => runSmells(bin, worktree))
    : { runs: [{ results: [] }] };

  const diff = classifyFindings(
    parseSarif(baseSarif),
    parseSarif(headSarif),
    args.allFiles ? null : changedFiles,
  );

  if (args.outDir) {
    mkdirSync(args.outDir, { recursive: true });
    writeFileSync(path.join(args.outDir, 'head.sarif'), JSON.stringify(headSarif));
    writeFileSync(path.join(args.outDir, 'base.sarif'), JSON.stringify(baseSarif));
    writeFileSync(
      path.join(args.outDir, 'findings.json'),
      JSON.stringify({ headSha, baseSha, changedFiles, ...diff }, null, 2),
    );
    writeFileSync(path.join(args.outDir, 'report.md'), markdownReport(diff, { headSha, baseSha }));
  }

  emit(diff, { headSha, baseSha });
  return totalFindings(diff) > 0 ? 1 : 0;
}

if (require.main === module) {
  try {
    process.exit(main());
  } catch (err) {
    console.error(`::error::${err.message}`);
    process.exit(2);
  }
}

module.exports = { parseArgs, resolveBase };
