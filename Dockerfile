FROM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY main.go ./
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/moerise . \
    && mkdir -p /out/data/state /out/data/index /out/backups

FROM scratch

ENV MOERISE_CONFIG_FILE=/run/moerise/config.json
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/moerise /usr/local/bin/moerise
COPY --from=build --chown=10001:10001 /out/data /data
COPY --from=build --chown=10001:10001 /out/backups /backups

USER 10001:10001
EXPOSE 8787
HEALTHCHECK --interval=15s --timeout=5s --start-period=20s \
    CMD ["/usr/local/bin/moerise", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/moerise"]
CMD ["serve"]
