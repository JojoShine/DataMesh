FROM golang:1.25.5-alpine@sha256:ac09a5f469f307e5da71e766b0bd59c9c49ea460a528cc3e6686513d64a6f1fb AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -trimpath -o /out/server ./cmd/server && CGO_ENABLED=0 go build -trimpath -o /out/manage ./cmd/manage && CGO_ENABLED=0 go build -trimpath -o /out/code-runner ./cmd/governance-code-runner && CGO_ENABLED=0 go build -trimpath -o /out/experience-ingress ./cmd/experience-ingress

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/server /app/server
COPY --from=build /out/manage /app/manage
COPY --from=build /out/code-runner /app/code-runner
COPY --from=build /out/experience-ingress /app/experience-ingress
COPY web/dist /app/web
USER 10001:10001
EXPOSE 8443
ENTRYPOINT ["/app/server"]
