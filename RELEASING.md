# Releasing

## Rolling edge build

1. Make sure tests pass: https://github.com/veertuinc/anklet-tests?tab=readme-ov-file#anklet-tests
2. Run https://github.com/veertuinc/anklet/actions/workflows/build-release-artifacts.yml
3. Leave **Publish full VERSION release** unchecked
4. The workflow replaces any existing `edge` GitHub release/tag and publishes a new prerelease tagged `edge` with `anklet_edge_*` zips

## Full VERSION release

1. Make sure tests pass: https://github.com/veertuinc/anklet-tests?tab=readme-ov-file#anklet-tests
2. Commit to `edge` (update the VERSION file!)
3. Run https://github.com/veertuinc/anklet/actions/workflows/build-release-artifacts.yml with **Publish full VERSION release** checked
4. The workflow builds zips, creates tag/release from `VERSION` (title `v$VERSION`, auto-generated notes, marked latest), and attaches the zips
5. The workflow fails if that tag or release already exists
