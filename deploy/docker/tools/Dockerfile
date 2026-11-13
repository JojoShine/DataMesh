FROM golang:1.25.5-alpine@sha256:ac09a5f469f307e5da71e766b0bd59c9c49ea460a528cc3e6686513d64a6f1fb
# Go's race detector requires a native C compiler; this stays in the tooling image.
RUN apk add --no-cache gcc=15.2.0-r2 musl-dev=1.2.5-r23
ENV GOMAXPROCS=2
