#!/usr/bin/env bash
#
# Run a command inside the test container built from test.Dockerfile, in the
# directory it was called from:
#
#   ./scripts/docker-test.sh go test -race ./...
#
# Every module's `make test` goes through this script. The container starts
# from a clean environment: no host variables, no ~/.aws or ~/.config, so a
# test that loads the machine's real AWS config fails here the way it fails
# in CI.
#
# The whole repository is mounted at /src so go.work resolves the local
# modules. The module and build caches live in named Docker volumes and
# survive between runs.
#
# The Docker socket is mounted because integration tests start their
# dependencies with testcontainers. TESTCONTAINERS_HOST_OVERRIDE points them
# at the host, where the dependency containers publish their ports.
#
# GAS_TEST_REQUIRE_DOCKER makes integration tests fail rather than skip if
# Docker turns out to be unreachable, since this script always provides it.

set -euo pipefail

if ! command -v docker >/dev/null 2>&1; then
	echo "docker-test: docker is required to run the tests" >&2
	exit 1
fi

root="$(git rev-parse --show-toplevel)"
prefix="$(git rev-parse --show-prefix)"
go_version="$(sed -n "s/^ *GO_VERSION: *'\(.*\)'/\1/p" "$root/.github/workflows/test.yml")"
if [ -z "$go_version" ]; then
	echo "docker-test: GO_VERSION not found in .github/workflows/test.yml" >&2
	exit 1
fi
image="gas-test:go$go_version"

# The build has no context: the image needs nothing from the repository.
docker build --quiet --build-arg "GO_VERSION=$go_version" -t "$image" - \
	<"$root/test.Dockerfile" >/dev/null

tty=()
if [ -t 0 ] && [ -t 1 ]; then
	tty=(-it)
fi

exec docker run --rm ${tty[@]+"${tty[@]}"} \
	-v "$root:/src" \
	-w "/src/$prefix" \
	-v gas-test-gomod:/go/pkg/mod \
	-v gas-test-gocache:/root/.cache/go-build \
	-v /var/run/docker.sock:/var/run/docker.sock \
	--add-host host.docker.internal:host-gateway \
	-e TESTCONTAINERS_HOST_OVERRIDE=host.docker.internal \
	-e GAS_TEST_REQUIRE_DOCKER=1 \
	"$image" "$@"
