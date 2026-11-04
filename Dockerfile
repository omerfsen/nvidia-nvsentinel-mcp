# The MCP server on distroless Python: no shell, no package manager, a non-root
# user. Everything is pinned: bases by digest, Python dependencies by hash, Go
# dependencies by go.sum. See docs/security.md for how to run it.
FROM python:3.13-slim-trixie@sha256:ae14d4ce8771606477a81446e7ef429edd6762305fc50c84507c1041fa8285d1 AS python-build
COPY requirements.txt /build/requirements.txt
COPY src/nvsentinel_mcp /app/nvsentinel_mcp
RUN pip install --no-cache-dir --disable-pip-version-check --root-user-action=ignore \
      --require-hashes --no-deps --only-binary=:all: --target /app -r /build/requirements.txt \
    && rm -rf /app/bin \
    && python -m compileall -q /app

# The official kubectl builds carry fixable HIGH CVEs in golang.org/x/net and
# x/text; this is the same command tree with those two updated (kubectl/go.mod).
FROM golang:1.27.3-trixie@sha256:c65ecfe613b539ac74c62a4c3b2c27e8f8a6076384c8f27e8a3c0f3be17cc385 AS kubectl-build
COPY kubectl /src
WORKDIR /src
RUN CGO_ENABLED=0 go build -mod=readonly -trimpath -ldflags='-s -w' -o /kubectl .

FROM gcr.io/distroless/python3-debian13@sha256:1c32c3720dc4c7f0c7dbe998737f32c200add6511eca7cec705008be488b87d6
COPY --from=python-build /app /app
COPY --chmod=555 --from=kubectl-build /kubectl /usr/local/bin/kubectl
ENV PYTHONPATH=/app \
    PYTHONDONTWRITEBYTECODE=1 \
    NVSENTINEL_MCP_KUBECTL=/usr/local/bin/kubectl \
    NVSENTINEL_MCP_KUBECONFIG=/kube/config \
    KUBECACHEDIR=/tmp/kube-cache \
    HOME=/tmp
USER 65532:65532
ENTRYPOINT ["/usr/bin/python3", "-P", "-m", "nvsentinel_mcp.server"]
