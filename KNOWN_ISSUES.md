# Known issues

Defects, test gaps, and debts found in Gas that are not fixed by the change
that found them. An entry stays here until the fix is merged; the fix removes
the entry in the same change, and its `CHANGELOG.md` entry records what was
fixed.

Each entry has a stable ID (`KI-NNN`, never reused), and these fields:

- **Status**: `open`, `investigating`, `decision needed` (a §3 decision in
  `CLAUDE.md` is pending), or `fix in progress` with the branch or PR.
- **Area**: the module and package.
- **Found**: the date, and how it was found.
- **Detail**: what is wrong, with the evidence (error text, file and line,
  input that triggers it).
- **Next step**: what has to happen to close it.

## KI-001: `auth/apikey` `TestAPIKeyListEmpty` failed on a missing port mapping

- **Status**: investigating
- **Area**: `auth/apikey`, `auth/internal/testutil`
- **Found**: 2026-10-15, one failure in a `make test-all` run on the
  uncommitted lifecycle, auth, and storage work.
- **Detail**: `apikey_integration_test.go:297: failed to get connection
  string: port "5432/tcp" not found` from a postgres:16-alpine
  testcontainer. It failed once; how often it fails, and whether it also
  fails on `main`, is not yet known.
- **Next step**: measure the failure rate on `main` and on the uncommitted
  work, find the cause, then fix it in its own `test(auth)` change or in
  the change that introduced it.

## KI-002: s3 backend has no real-service test suite after the storage rewrite

- **Status**: decision needed
- **Area**: `storage/s3`
- **Found**: 2026-10-15, while splitting the uncommitted work into PRs.
- **Detail**: `main` tests the s3 backend against LocalStack
  (`storage/s3/integration_test.go`). The pending storage rewrite deletes
  that suite and drops testcontainers from `storage/go.mod`, so the s3
  backend would be tested only through mocks and fakes, which §2 of
  `CLAUDE.md` does not accept. MinIO is ruled out by its license, and no
  replacement container has been chosen.
- **Next step**: choose the S3-compatible container for the suite, then
  add it back.
