# Contributing

Thanks for your interest in the Driverforge CLI! Contributions are welcome under the project's [MIT license](LICENSE).

## How this repository works

This repository is a **public mirror**. The CLI is developed in a private repository and synced here automatically with [Copybara](https://github.com/google/copybara). Your workflow is the normal GitHub one — open issues and pull requests here as usual.

## Why your merged PR shows as "closed", not "merged"

When we accept your change it will show as **closed** (red), not **merged** (purple). That's expected, not a rejection: we import your PR into our private repository, review and test it there, and merge it internally. The change is then synced back out to this repo, and GitHub closes the original PR.

**Your authorship is preserved** — your commits appear here with you as the author, so they still show on your GitHub profile.

**How to tell an accepted PR from a declined one:** when your change lands, our bot labels the PR **`accepted`** and comments with a link to the commit where it landed in this repo. A closed PR *without* that label was declined — and we'll say why.

## Contributor License Agreement

Contributions require agreeing to our Contributor License Agreement: you keep the copyright to your contribution and grant Driverforge — and its successors and assigns — a licence to use and relicense it. A CLA check runs on your pull request.

**AI assistance is welcome.** However your contribution was produced, you're responsible for it: it must be yours to give, and you must have the right to contribute it under this licence (for example, it must not carry in incompatibly-licensed code). We don't require AI use to be disclosed or attributed — your responsibility for the contribution is the same either way.

## Commit, issue & PR conventions

### Commits

We use [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>: <subject> (#<issue-or-pr>)
```

e.g. `fix: render a build failure once, as a red failure card (#78)`.

- **Types:** `feat`, `fix`, `docs`, `refactor`, `tweak` (a small change below a feature), `chore` (deps / no-op).
- Use the **imperative mood** — "add", not "added".
- Reference the related GitHub issue/PR number in parentheses; it can be left out until the PR is opened.
- Keep commits small and logical, with messages that say what changed and why.

### Issues

For anything beyond a trivial fix, please open an issue first describing the bug or feature and its use case — it's the best place to agree on the design before code is written. For bugs, include your OS, `driverforge --version` output, and steps to reproduce where you can.

### Pull requests

- Title the PR in the same format as a commit (e.g. `fix: handle a missing squishy file gracefully`).
- Keep it small and focused; for larger changes, open an issue first.
- Include tests for fixes and new behaviour.

## Building and testing

A recent [Go](https://go.dev/) toolchain (see `go.mod` for the minimum version) and `make`:

```sh
make build   # build the driverforge binary
make test    # run all tests
make check   # gofmt check + go vet + tests — run this before pushing
```

### Maintainability gate

A [qlty](https://qlty.sh) maintainability gate runs on every PR once it reaches
our internal repository, so you will not see it on your pull request here. It
fails on any code smell — complexity,
duplication, long parameter lists — in a file your branch changed, **including
smells your branch did not write**: editing a file makes its debt yours to clean
up. Thresholds live in `.qlty/qlty.toml`.

Reproduce it locally with:

```sh
make qlty   # same verdict CI will reach
```

`make qlty` needs the qlty binary. Install the version CI pins from
[qlty's releases](https://github.com/qltysh/qlty/releases) and point `QLTY_BIN`
at it — the Homebrew tap lags well behind, so `brew install qltysh/tap/qlty`
lands on a build the gate refuses and `brew upgrade` reports it as already
current. `make qlty` tells you the required version if yours is too old.

Findings are reported in full in the job log and on the job summary — nothing is
behind a login, and you don't need an account anywhere but GitHub.

They are also posted as a PR comment, but **not on pull requests from forks**:
GitHub gives those runs a read-only token, so the comment can't be written. That
step is allowed to fail without failing the check, so a fork PR's `Qlty` verdict
still reflects your code and nothing else — read the job summary for the detail.

## Code style

- Code is `gofmt`-formatted (`make fmt`); CI enforces `make check`.
- **Error handling follows a strict split:**
  - Expected, user-facing conditions are modelled as `internal/clierr.UserError` (title + message + optional hint) built with the constructors (`clierr.User`/`UserT`/`Wrap`/`WrapT`/`Silent`) — never a bare struct literal. They render as a friendly card.
  - Any other error that reaches the top is treated as a bug (crash card). When a command anticipates a condition, translate it into a `UserError` rather than letting a raw error bubble up.
  - In the non-`cli` packages (`internal/*`, `pkg/*`), wrap errors with `fmt.Errorf` using a lowercase context prefix and no trailing punctuation (e.g. `"agent: …"`, `"read manifest: …"`) so a raw error names its origin if it surfaces.
- The build library (`pkg/c4z`) is UI-agnostic — progress is reported through plain event values; keep TUI concerns in `internal/ui`.
- Golden tests freeze the `.c4z` output format (`go test ./pkg/c4z -run Golden`); if you intentionally change the output, regenerate with `-update` and explain why in the PR.
