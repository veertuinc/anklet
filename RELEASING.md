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
4. Create the GitHub release and attach the workflow zips (and checksum file if present)
