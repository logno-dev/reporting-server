# syntax=docker/dockerfile:1
FROM node:22-bookworm-slim AS web-build
WORKDIR /src/web/template-manager
COPY web/template-manager/package.json web/template-manager/package-lock.json ./
RUN npm ci
COPY web/template-manager ./
RUN npm run build

FROM golang:1.24-bookworm AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY --from=web-build /src/internal/web/dist ./internal/web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/report-server ./cmd/server

FROM debian:bookworm-slim
ARG TYPST_VERSION=0.13.1
ARG TARGETARCH
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl xz-utils \
    && case "$TARGETARCH" in amd64) typst_arch=x86_64 ;; arm64) typst_arch=aarch64 ;; *) exit 1 ;; esac \
    && curl -fsSL "https://github.com/typst/typst/releases/download/v${TYPST_VERSION}/typst-${typst_arch}-unknown-linux-musl.tar.xz" -o /tmp/typst.tar.xz \
    && tar -xJf /tmp/typst.tar.xz -C /tmp \
    && install "/tmp/typst-${typst_arch}-unknown-linux-musl/typst" /usr/local/bin/typst \
    && rm -rf /var/lib/apt/lists/* /tmp/typst* \
    && useradd --system --uid 10001 --create-home report
WORKDIR /app
COPY --from=go-build /out/report-server /usr/local/bin/report-server
COPY typst ./typst
RUN mkdir -p /data/reports && chown -R report:report /data
USER report
ENV HTTP_ADDRESS=:8080 \
    TYPST_ROOT=/app/typst \
    REPORTS_DIRECTORY=/data/reports \
    RENDERER_VERSION=typst-0.13.1
EXPOSE 8080
ENTRYPOINT ["report-server"]
