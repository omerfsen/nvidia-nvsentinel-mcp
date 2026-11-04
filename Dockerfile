# The MCP server as one static binary on distroless: no shell, no package manager, no
# libc, a non-root user. Bases are pinned by digest, Go dependencies by go.sum. The base
# adds about 2 MB, and -s -w strips the debug data. See docs/security.md to run it.
FROM golang:1.27.3-trixie@sha256:c65ecfe613b539ac74c62a4c3b2c27e8f8a6076384c8f27e8a3c0f3be17cc385 AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum main.go ./
COPY pkg ./pkg
RUN CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=readonly go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" -o /nvidia-nvsentinel-mcp .

FROM gcr.io/distroless/static-debian13@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
COPY --chmod=555 --from=build /nvidia-nvsentinel-mcp /nvidia-nvsentinel-mcp
ENV NVSENTINEL_MCP_KUBECONFIG=/kube/config
LABEL io.modelcontextprotocol.server.name="io.github.omerfsen/nvidia-nvsentinel-mcp"
USER 65532:65532
ENTRYPOINT ["/nvidia-nvsentinel-mcp"]
