# Security Policy

## Reporting a vulnerability

Please report security issues **privately** — do not open a public issue.

- **Preferred:** GitHub's [private vulnerability reporting](https://docs.github.com/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability) on this repository (**Security → Report a vulnerability**).
- **Or** email **oss@driverforge.com**.

Please include a description, reproduction steps, and the affected version (`driverforge --version`) where you can. We'll acknowledge your report and keep you posted on the fix.

## Scope

This policy covers the **Driverforge CLI** in this repository — the `driverforge` binary that builds, deploys, and manages Control4 drivers.

Security-relevant surfaces worth knowing about when assessing an issue:

- **Credentials.** The CLI is a public OAuth client (device authorization grant); it holds no client secret and never sees user passwords. Tokens are stored in the OS secret store where available, falling back to a `0600` file. Anything that exposes stored tokens, weakens the storage fallback, or leaks a token into logs/output is in scope.
- **Remote input.** Output from controllers and APIs is rendered into the user's terminal; the CLI strips terminal control sequences from remote-sourced strings at ingestion. Bypasses of that sanitization are in scope.
- **Archives.** The CLI reads and writes `.c4z` (zip) archives; path-traversal or extraction issues are in scope.

The Driverforge backend services, the Control4 agent drivers, and the SDKs are separate projects and out of scope here — but if you find a server-side issue via the CLI, please still report it through the channels above and we'll route it.

## Supported versions

We support the latest released version — please confirm the issue on the current release (`brew update && brew upgrade driverforge` or see the [install docs](https://go.driverforge.com/install)) before reporting.
