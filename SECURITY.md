# Security Policy

## Reporting a vulnerability

Please report security issues privately, never in a public issue.

- Use **GitHub Security Advisories**: open the repository's "Security" tab →
  "Report a vulnerability".
- If that is not available, open a private advisory draft any other way you
  trust this project to receive.

Include: affected version (`creator version`), what you did, what you expected,
what happened, and a minimal reproduction if you have one.

## Supported versions

| Version | Supported |
| --- | --- |
| v0.1.x | yes |

Creator is pre-1.0. Fixes land in a new tagged release; there are no paid
backport branches.

## Credentials you hand to Creator

Creator never asks for a key unless you run `creator connect <provider>` or set
an environment variable for the provider you use.

- API keys live in `~/.creator/keys.json` (user-only file permissions) or in
  your environment. **They are never written into a repository**, and nothing
  in this repo expects one.
- The installers and CI/release workflows collect no secrets, no analytics, no
  telemetry.
- If you ever suspect a key leaked into a file, rotate it at the provider first,
  then tell us — do not open a public issue.

## Design boundaries (please do not fuzz these)

- `shell` runs **as your user**. The policy engine (AST parsing + policy
  verdicts) is a seatbelt, not a sandbox.
- Structured file tools (`read`, `write`, `edit`, `patch`, …) are confined to
  the workspace, including symlink resolution.
- Workspace Lua plugins are inert until you pass `--workspace-plugins` or set
  `CREATOR_WORKSPACE_PLUGINS=1`. Opening an untrusted repository with plugins
  enabled never runs them.
- Network tools (`net_fetch`, `web_fetch`) refuse loopback and private targets
  unless you set `CREATOR_ALLOW_PRIVATE_NET=1` — only do that on trusted
  networks.
