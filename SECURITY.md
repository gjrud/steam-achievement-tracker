# Security Policy

## Supported versions

Only the latest commit on `main` is supported. Fixes are not backported.

## Reporting a vulnerability

Please do **not** open a public issue for security problems.

Report privately through GitHub:
[Report a vulnerability](https://github.com/gjrud/steam-achievement-tracker/security/advisories/new)
(repository **Security** tab → **Report a vulnerability**).

Include:

- Description of the issue and its impact
- Steps to reproduce or a proof of concept
- Affected commit, version, or build
- Your environment (Linux distribution, Wails version) if relevant

## What to expect

- Acknowledgement within 7 days.
- Status updates as the report is triaged and fixed.
- Credit in the advisory if you want it.

This is a hobby project maintained on a best-effort basis; there is no bug bounty.

## Scope

In scope: this repository's Go backend, Svelte frontend, and build/CI configuration.

Particularly relevant areas:

- Handling of the Steam Web API key (entered in the app, stored only in the Linux Secret Service, never logged)
- Local data under `~/.steam-achievement-tracker`
- Network requests to Steam

Out of scope: vulnerabilities in Steam itself, Wails, or other upstream dependencies. Report those to the respective projects.
