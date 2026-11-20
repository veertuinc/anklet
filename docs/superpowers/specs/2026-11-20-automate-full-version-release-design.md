# Automate full VERSION GitHub release

Date: 2026-11-20  
Status: approved design

## Problem

`Build Release Artifacts` with **Publish full VERSION release** checked builds zips and uploads a workflow artifact, but does not create a GitHub release or push a version tag. Edge already publishes via `gh release create`. Full release still needs a manual attach step in `RELEASING.md`.

## Goals

- When `full_release` is true, the workflow publishes a GitHub release for the VERSION file value.
- Fail if that tag or release already exists (no overwrite).
- Keep GoReleaser as build-only (`release.disable: true`).
- Match existing release naming: tag `2.2.0`, title `v2.2.0`.
- Update `RELEASING.md` so full release is workflow-only (no manual attach).

## Non-goals

- Enable GoReleaser’s built-in GitHub release publisher.
- Change edge rolling-release behavior.
- Change VERSION format or binary/zip naming templates.

## Approach

Mirror the edge publish step: after GoReleaser succeeds on the full-release path, run a new **Publish VERSION GitHub release** step with `gh`.

## Workflow behavior (`full_release: true`)

1. Checkout `inputs.ref` (unchanged).
2. Create a local git tag from `VERSION` (unchanged; required for GoReleaser version).
3. Run GoReleaser `release --clean` (unchanged; GitHub publish remains disabled in `.goreleaser.yml`).
4. **Publish VERSION GitHub release** (new):
   - `version=$(tr -d '[:space:]' < VERSION)`
   - Fail if `gh release view "$version"` succeeds.
   - Fail if `git ls-remote --tags origin "refs/tags/${version}"` returns a matching tag.
   - Require at least one `dist/*.zip`; fail with a clear error if none.
   - Create release:
     ```bash
     gh release create "$version" \
       --title "v${version}" \
       --generate-notes \
       --latest \
       --target "$(git rev-parse HEAD)" \
       "${assets[@]}"
     ```
   - Use `GH_TOKEN: ${{ steps.app-token.outputs.token || github.token }}` (same as edge).
5. Upload workflow artifacts (unchanged).

## Edge path

Unchanged: delete/recreate `edge` prerelease after snapshot build.

## Docs

Update **Full VERSION release** in `RELEASING.md`:

1. Tests pass.
2. Commit VERSION (and code) to `edge`.
3. Run the workflow with **Publish full VERSION release** checked.
4. Workflow publishes tag + release with zips and generated notes.

Remove the manual “create release and attach zips” step.

## Error cases

| Case | Result |
|------|--------|
| Empty `dist/*.zip` | Step fails |
| Release `$version` already exists | Step fails |
| Remote tag `$version` already exists | Step fails |
| Missing App token | Falls back to `github.token` (same as edge) |

## Success criteria

- Dispatch with `full_release: true` and a new VERSION creates a non-prerelease GitHub release marked latest, with auto-generated notes and zip assets.
- Second dispatch for the same VERSION fails without changing the existing release.
- Edge dispatch without `full_release` still replaces the rolling `edge` release.
