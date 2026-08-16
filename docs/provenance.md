# Provenance: relationship to Control4's DriverPackager

The Driverforge CLI's `.c4z` build pipeline (`pkg/c4z`) is an **independent
implementation, written from scratch in Go**. It is deliberately
*format-compatible* with the Control4/Snap One driver toolchain: the Python
[`drivers-driverpackager`](https://github.com/snap-one/drivers-driverpackager)
published publicly by Snap One, plus the `squish` Lua bundler and OpenSSL
S/MIME encryption it shells out to. That compatibility exists so existing
driver projects build identically. This document records what it does and does
not involve.

## What "format-compatible" means here

- **No code was ported.** The Go implementation shares no code, structure, or
  identifiers with the Python tool. Where the Control4 tool's *output format*
  is the contract, the Go code reproduces that format, not the code that
  produced it. Those formats are the `.c4zproj` manifest schema, the `squishy`
  directive format, the squish bundle layout (`package.preload[...]` preamble
  wrapping), the `.encrypted` entry naming, and the `driver.xml` `<modified>`
  timestamp format.
- **Compatibility targets observable behaviour.** The reference for every
  compatibility decision was the publicly available tool's inputs and outputs.
  Known bugs in the Control4 implementation were deliberately **not**
  reproduced; where one of its quirks affects output bytes, the parity decision
  is documented at the relevant code site.
- **The embedded certificate is public interoperability material.** Drivers
  built with `encryption="2"` are enveloped to Control4's "DriverWorks
  Encryption V2" **public** certificate (`pkg/c4z/encrypt/cert.go`). A public
  key certificate exists to be distributed; this one ships in every copy of
  the publicly published Control4 tool. The corresponding private key is held
  by Control4 and has never been part of this project. The envelope itself is
  produced with the maintained pure-Go
  [`smallstep/pkcs7`](https://github.com/smallstep/pkcs7) library using the
  same standard algorithms (AES-256-CBC content encryption, RSA key
  transport).
- **Encryption here is packaging, not secrecy from the platform.** Script
  "encryption" is a Control4 platform feature: it protects driver source in
  transit/at rest from casual extraction, and only Control4 directors can
  decrypt it. This project implements the *producing* side only, exactly as
  the platform intends third-party driver developers to do.
