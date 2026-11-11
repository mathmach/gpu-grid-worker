# Security Policy

## Scope

This repository covers the open-source **GPU Grid Worker** daemon, desktop GUI, local loopback control plane (`127.0.0.1:17771`), content-addressed storage (`CAS`), archive extraction guards, and subprocess runners.

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

Instead, use [GitHub Private Vulnerability Reporting](https://github.com/mathmach/gpu-grid-worker/security/advisories/new) on this repository to submit a confidential report with:
1. Affected package/file path and commit hash or release tag.
2. Minimal reproduction steps or proof-of-concept.
3. Expected vs. actual security boundary behavior (e.g., SSRF, Zip Slip, symlink escape, CSRF/DNS rebinding on `127.0.0.1:17771`, or subprocess env leakage).

We acknowledge reports within 72 hours and coordinate fixes before public disclosure.
