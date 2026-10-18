# Image that `make test` runs go test in, so tests never see the host's
# environment, ~/.aws, or other credentials. Built and run by
# scripts/docker-test.sh, which passes GO_VERSION from .github/workflows/test.yml
# so local runs use the same Go as CI.
ARG GO_VERSION
FROM golang:${GO_VERSION}

# Use the image's Go even when a go.mod asks for a newer toolchain, instead of
# downloading one per run.
ENV GOTOOLCHAIN=local
