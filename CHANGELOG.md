# Changelog

## v0.3.1

### Changed (behaviour)

- Rejected requests are no longer recorded in the sliding window by default. Previously every call
  was added to the ZSET before the limit check, so a client that kept sending requests while blocked
  stayed blocked indefinitely. Now only allowed requests count.
- To keep the old behaviour set `Config.CountRejected = true`. Existing consumers
  (widgetapi, payin-processing) that relied on "hammering extends the block" must opt in explicitly.

### Added

- `Config.CountRejected` (default `false`).

### Fixed

- The window update is now a single atomic Lua script instead of a pipeline, so concurrent callers
  can no longer overshoot the limit between the check and the write.
- README and CLAUDE.md describe the current API and the atomic implementation.

## v0.3.0

- `Limiter` is no longer generic; strategy keys are `fmt.Stringer`.
