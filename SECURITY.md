# Security policy

## Reporting a vulnerability

Please report security problems privately, never in a public issue or pull request:

- e-mail **security@openbrandexchange.com**, or
- open a private report under **Security → Report a vulnerability** on this GitHub repository.

Include what you found, the affected version (`obx version`, `obx-conformance version`, or the
module version of the Go SDK), the steps to reproduce it and the impact you expect. Please do not
access data that is not yours, and give us time to fix the problem before you disclose it.

## What happens next

| When | What we do |
|---|---|
| within 3 working days | confirm that we received the report |
| within 10 working days | tell you whether we can reproduce it and how serious we think it is |
| within 90 days (sooner for critical issues) | release a fix and publish an advisory, crediting you if you wish |

We coordinate the date of public disclosure with you. There is no bug bounty.

## Supported versions

Security fixes are made for the latest minor release (0.1.x today). Pin a version in the GitHub
Action (`version: v0.1.0`) and update when a release says it contains a security fix.

## Scope

This repository: the `obx` command, the GitHub Actions (`push-action`, `conformance-action`), the
Go SDK packages, `obx-conformance` and its mock portal, the release archives, the container image
`ghcr.io/openbrandexchange/obx` and the Homebrew formula. Problems in the protocol itself go to the
spec repository's private reporting in the same way; problems in a portal edition to that edition's
repository. When in doubt, e-mail us and we route it.

## How obx handles secrets

- Access keys stored by `obx login` live in the user's settings file with mode 0600
  (`obx config` shows where); `obx config` and the logs show only a shortened key.
- `obx login` with e-mail and password never stores the password; it creates an access key and
  signs out again.
- Every downloaded file is checked against its sha256 (`obx pull`, the SDK's `client.Download`);
  the GitHub Action checks the obx archive against the release's `checksums.txt`.
- The mock portal refuses webhook and import addresses that point to loopback, private, link-local
  or cloud-metadata addresses, and checks the address it actually connects to (no DNS rebinding);
  the development switch only allows loopback and private addresses, never link-local ones.
- Release artifacts come with SHA-256 checksums, SPDX SBOMs and a build provenance attestation.
