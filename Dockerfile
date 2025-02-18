FROM docker.io/alpine:3.21.3 AS build

ARG QBITTORRENT_VERSION=5.0.4
ARG LIBTORRENT_VERSION=2.0.11

WORKDIR /tmp

COPY qbittorrent-startup.go cross-seed.go ./

WORKDIR /app

ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64

RUN apk add --update --no-cache \
        ca-certificates-bundle \
        catatonit \
        tzdata \
        go \
        build-base \
    && go build -o ./qbittorrent-startup /tmp/qbittorrent-startup.go \
    && go build -o ./cross-seed /tmp/cross-seed.go \
    && wget -q "https://github.com/userdocs/qbittorrent-nox-static/releases/download/release-${QBITTORRENT_VERSION}_v${LIBTORRENT_VERSION}/x86_64-qbittorrent-nox" -O qbittorrent-nox \
    && chmod -R 755 ./

FROM scratch

WORKDIR /app

ENV QBT_CONFIRM_LEGAL_NOTICE=1 \
    QBT_WEBUI_PORT=8080 \
    QBT_TORRENTING_PORT=50413 \
    HOME="/config" \
    XDG_CONFIG_HOME="/config" \
    XDG_DATA_HOME="/config" \
    SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt

COPY --from=build /usr/bin/catatonit /usr/bin/catatonit
COPY --from=build /app ./
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY ./qBittorrent.conf ./qBittorrent.conf

VOLUME /config

EXPOSE 8080

ENTRYPOINT ["catatonit", "--", "/app/qbittorrent-startup"]
