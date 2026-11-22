# 0143. Write the Homebrew tap with the organization's GitHub App

- Status: Accepted
- Date: 2026-11-22
- Issue: none; introduced by PR #143 (ADR 0119, decision 3, is amended by this record)

## Context
ADR 0119 made the release job require a repository secret `HOMEBREW_TAP_GITHUB_TOKEN`, a personal access token with write access to `stokaro/homebrew-kc`. A personal token belongs to one person and expires on a date somebody picked. The `stokaro` organization already has the `ptah-publish` GitHub App, installed on all its repositories with contents write access, and ptah writes its own tap (`stokaro/homebrew-ptah`) with it.

## Decision
The release job mints an installation token of `ptah-publish` with `actions/create-github-app-token`, limited to `stokaro/homebrew-kc`, from the organization variable `PUBLISH_APP_ID` and secret `PUBLISH_APP_KEY`. Both must be shared with `stokaro/kc`. `HOMEBREW_TAP_GITHUB_TOKEN` stays as a fallback; the job prints which credential it used and still fails before publishing when there is neither. GoReleaser reads the resolved token from the same `HOMEBREW_TAP_GITHUB_TOKEN` environment variable, so `.goreleaser.yaml` is unchanged.

## Alternatives considered
- Keep the personal token only: works, but ties releases to one account and an expiry date.
- The `ptah-automation` app, whose ID and key are already shared with all repositories: it also has issues write access, which the tap does not need. `ptah-publish` is the narrower one and the one ptah uses for the same job.

## Consequences
An organization admin has to share `PUBLISH_APP_ID` and `PUBLISH_APP_KEY` with `stokaro/kc` once. The installation token lives an hour and the release job is capped at 40 minutes, so the token cannot expire during a run. Once a release has published through the app, the fallback and the `HOMEBREW_TAP_GITHUB_TOKEN` secret can be removed.
