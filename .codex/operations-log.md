# Operations Log

## 2026-06-07 axonhub/llm dependency upgrade

- User authorized a cautious upstream compatibility check and dependency update.
- Verified upstream update with `go list -m -u -json github.com/looplj/axonhub/llm`.
- Upgraded `github.com/looplj/axonhub/llm` from `v0.0.0-20260604152216-28adad0ac550` to `v0.0.0-20260607053355-98c7855a88cf`.
- Ran `go mod tidy`.
- Ran `go test -count=1 ./...`; all Go packages passed.
- Checked `git diff -- go.mod go.sum`; dependency diff is limited to the axonhub/llm version and checksum.

## 2026-06-07 fork update-source cleanup

- Removed the frontend Settings Info latest-version/update prompt for this forked build.
- Changed the default frontend repository link from `bestruirui/octopus` to `linbmv/octopus`.
- Changed backend update release URLs and version metadata to `linbmv/octopus` so direct update API calls do not pull the original upstream release.
