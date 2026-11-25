FROM node:24-alpine@sha256:8a32c8cf649a49915a66e81e7a609223c708d0adabf288ee3f4077d765542f85 AS web
WORKDIR /web
COPY web/package.json web/build.mjs ./
COPY web/src ./src
RUN node build.mjs

FROM golang:1.27.4-bookworm@sha256:ce59b41415e2524ae99926fe1f37ce48ba8c194dcbd2a56a65436cdcefa2bd34 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY main.go ./
COPY internal ./internal
COPY web/assets.go ./web/
COPY --from=web /web/dist ./web/dist
ARG PROJECT_TELEMETRY_RECEIVER
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X moerise/internal.telemetryReceiverURL=${PROJECT_TELEMETRY_RECEIVER}" -o /out/moerise . \
    && mkdir -p /out/data/state /out/data/index /out/backups

FROM scratch

ENV MOERISE_CONFIG_FILE=/run/moerise/config.json
COPY LICENSE /usr/share/licenses/moerise/LICENSE
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
