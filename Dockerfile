ARG GO_BUILD_IMAGE=golang:1.25.5-alpine
FROM ${GO_BUILD_IMAGE} AS build
ENV GOMAXPROCS=2 GOGC=50
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    set -eu; mkdir -p /out/bin && \
    for program in server manage governance-code-runner experience-ingress runtime-tools; do \
      CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/$program ./cmd/$program; \
    done && \
    mv /out/governance-code-runner /out/code-runner && \
    for task in healthcheck migrate bootstrap; do ln -s /app/runtime-tools /out/bin/$task; done
COPY web/dist /out/web
ARG BASE_PATH=/datamesh
RUN grep -F "src=\"${BASE_PATH}/static/" /out/web/index.html >/dev/null

FROM build AS verify
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go test ./cmd/... ./internal/... ./migrations && go vet ./...

FROM scratch AS runtime
ARG SOURCE_REVISION
ARG RELEASE_VERSION
LABEL org.opencontainers.image.source="https://github.com/JojoShine/DataMesh" \
      org.opencontainers.image.revision=$SOURCE_REVISION \
      org.opencontainers.image.version=$RELEASE_VERSION \
      org.opencontainers.image.licenses="NOASSERTION"
COPY --from=verify /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=verify /out /app
COPY migrations/*.sql /app/migrations/
USER 10001:10001
WORKDIR /app
EXPOSE 8080
CMD ["/app/server"]
