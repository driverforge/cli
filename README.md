# Driverforge CLI

[![CI](https://badge.buildkite.com/c3dfa0a79e8ed68f9f0e34cd087ff41800eae7ab37de1b4980.svg?branch=main)](https://buildkite.com/kmsgroup/cli)

`driverforge` builds, deploys, and live-develops **Control4 drivers**: it
packages `.c4z` artifacts (a single-binary alternative to the Python
`driverpackager` + `luajit squish` + OpenSSL toolchain), deploys them to
controllers, and hot-swaps Lua without a device reload.

**📖 Using the CLI?** Installation, commands, and usage guides live at
**<https://go.driverforge.com/cli>** — this README is for people
working **on** the CLI itself.

## Prerequisites

- A recent [Go](https://go.dev) toolchain — the minimum version is pinned in
  [`go.mod`](go.mod)
- `make`

No cgo, no other system dependencies: `CGO_ENABLED=0` builds everywhere.

## Build

```sh
make build        # ./driverforge for the host platform
make install      # go install into $GOBIN / ~/go/bin
make release      # cross-compile all platforms into dist/ (not what ships; see below)
make help         # everything else
```

Version metadata (`driverforge version`) is stamped via `-ldflags -X` into
`internal/buildinfo`; a plain `go build ./cmd/driverforge` works too and
reports itself as a dev build. `make build` takes the version from
`git describe`, which only knows the tags in your clone and can be well behind
the published release. Set it when the version matters, for example to test a
build that `driverforge upgrade` and the update notice should treat as newer:

```sh
make build VERSION=1.0.2-next.$(git rev-parse --short HEAD)
```

The production endpoints (auth, API, telemetry) are compiled-in defaults that
can be overridden per build (`make build-dev` targets a local dev stack) or
per run via `DRIVERFORGE_*` env vars — see `internal/*/endpoints.go`. Metrics
and crash-reporting sinks default **empty** in source ("wired but dark"):
a source build never sends telemetry anywhere. Usage metrics are opt-in
(prompted once) and associated with the signed-in Driverforge account.

### Release builds

Releases are built by [GoReleaser](https://goreleaser.com) from
[`.goreleaser.yaml`](.goreleaser.yaml), not by `make release`. Beyond the
version, a release build bakes in the crash-reporting DSN, the usage-metrics
token and the `go.driverforge.com` short-link base; everything else is the
same code `make build` compiles. To produce release-shaped binaries locally
without publishing anything:

```sh
GORELEASER_CURRENT_TAG=v1.0.1 goreleaser build --snapshot --clean --single-target   # → dist/, version 1.0.2-next
```

A snapshot is versioned as the next patch after the current tag, which
GoReleaser also reads from `git describe`; pin it to the latest published
release (`driverforge --version` on an installed copy; `v1.0.1` above is an
example) or the snapshot will carry an old version. The same applies to the
`VERSION` values in the `make build` examples.

Leave the DSN and token unset (as above) for a local build, so it reports
nothing anywhere.

To try an unreleased build next to the one you have installed, give it a
different name rather than replacing yours:

```sh
make build VERSION=1.0.2-next.$(git rev-parse --short HEAD)
install -m 0755 driverforge "$(go env GOPATH)/bin/driverforge-next"
```

Don't let `driverforge upgrade` update the CLI from such a build: it replaces
the running binary with the published release.

## Test

```sh
make test         # go test ./...
make check        # gofmt check + go vet + tests — run before pushing
```

Notable suites:

- **Golden tests** (`go test ./pkg/c4z -run Golden`) freeze the `.c4z` output
  format byte-for-byte — archive contents, squished Lua, transformed
  driver.xml, source map. If you intentionally change the output, regenerate
  with `-update` and justify it in the PR.
- Integration-style CLI tests live next to the commands in `internal/cli`.

## Layout

```
cmd/driverforge/     entrypoint
internal/cli/        Cobra command wiring (thin adapters over the libraries)
internal/ui/         terminal UI (Bubble Tea) + plain non-TTY renderer
internal/auth/       OAuth2 device flow + token storage (keychain, 0600 fallback)
internal/agent/      client for the on-controller agent (deploy, sync, logs)
internal/driverforgeapi/  Driverforge GraphQL API client
internal/telemetry/  live driver telemetry (Centrifugo websocket)
internal/telemetryread/  reading telemetry back: entry contract, filters,
                     paging, wait/follow (logs, errors, events, get)
internal/agentmode/  detects a coding agent, and when output is structured
internal/skills/     agent skills shipped in the binary (driverforge skills install);
                     tests in internal/cli hold them to the CLI's flags and codes
internal/clierr/     UserError — expected, user-facing failures
internal/errreport/  opt-in crash reporting
pkg/c4z/             the build library: manifest, squish, driverxml,
                     pack, encrypt, sourcemap
```

`pkg/c4z` is deliberately UI-agnostic and reusable: it imports no CLI/TUI
code and reports progress through a `Progress func(Event)` callback.

Two conventions worth knowing before your first change (details in
[CONTRIBUTING.md](CONTRIBUTING.md)):

- **Errors:** anticipated conditions become `clierr.UserError` (friendly
  card, never crash-reported); anything else surfacing at the top is treated
  as a bug. Library packages prefix wrapped errors with a lowercase context
  tag.
- **Structured output:** under a coding agent, or with `DRIVERFORGE_OUTPUT=json`,
  every command writes one JSON document to stdout and never prompts — write
  text through `textOut`, report results with `emit`.
- **Output:** strings from controllers/APIs are sanitized of terminal escape
  sequences at ingestion (`internal/sanitize`) — keep remote data flowing
  through the existing decode paths.

## Dependencies

Pure-Go, permissively licensed (MIT/BSD/Apache-2.0). The main ones:
[cobra](https://github.com/spf13/cobra) (commands),
[Bubble Tea](https://github.com/charmbracelet/bubbletea)/Charm (TUI),
[smallstep/pkcs7](https://github.com/smallstep/pkcs7) (driver script
encryption), [go-keyring](https://github.com/zalando/go-keyring) (token
storage), [golang.org/x/oauth2](https://pkg.go.dev/golang.org/x/oauth2)
(device flow), [centrifuge-go](https://github.com/centrifugal/centrifuge-go)
(telemetry), [sentry-go](https://github.com/getsentry/sentry-go) (opt-in
crash reports).

Everything linked into the binary is inventoried in
[THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md); after changing
dependencies, regenerate it with `./scripts/gen-third-party-licenses.sh`.

## Relationship to Control4's DriverPackager

Control4 driver projects have long been built with Snap One's
[drivers-driverpackager](https://github.com/snap-one/drivers-driverpackager)
tooling — Python scripts orchestrating `squish` for Lua bundling and OpenSSL
for script encryption. `driverforge build` is a from-scratch Go
implementation of the same packaging formats: `.c4zproj` manifests, `squishy`
bundles, the `driver.xml` transforms, and `encryption="2"` script encryption
(to the same Control4 public certificate, so Director accepts the output).
An existing driver project builds identically with either tool; the golden
tests pin our output byte-for-byte.

What's shared is the file formats, not the code — this codebase borrows
nothing from the Python implementation, and a few of its quirks are
deliberately not reproduced (noted in code comments where behaviour
diverges). The `c4i` driver type and Lua minification are out of scope.

Driverforge is an independent project: not a Control4 or Snap One product,
and not affiliated with or endorsed by them. Control4, Composer, and
DriverWorks are trademarks of their respective owners.

## Releases

Releases are cut by tagging `vX.Y.Z`; CI (GoReleaser) builds, publishes to
`releases.driverforge.com`, and updates the Homebrew tap and Scoop bucket.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the workflow, commit conventions,
and code style, [SECURITY.md](SECURITY.md) for reporting vulnerabilities, and
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) for community standards.

## License

[MIT](LICENSE) © Driverforge Pty Ltd

Driverforge is a trademark of Driverforge Pty Ltd. The MIT licence covers the
code, not the name: forks and redistributions are welcome, but must not use the
Driverforge name or marks in a way that implies affiliation or endorsement.
