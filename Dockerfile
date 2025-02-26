FROM cgr.dev/chainguard/wolfi-base:latest@sha256:7afaeb1ffbc9c33c21b9ddbd96a80140df1a5fa95aed6411b210bcb404e75c11 AS build

# renovate: datasource=github-releases depName=userdocs/qbittorrent-nox-static
ARG VERSION=release-5.0.4_v2.0.11

WORKDIR /rootfs

ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64

COPY qbittorrent-startup.go cross-seed.go /tmp

RUN apk add --no-cache \
        tzdata \
        go \
        gpg \
        gpg-agent \
        gnupg-dirmngr \
        curl && \
    mkdir -p app/bin usr/bin etc && \
    echo 'qbittorrent:x:65532:65532::/nonexistent:/sbin/nologin' > etc/passwd && \
    echo 'qbittorrent:x:65532:' > etc/group && \
    go build -o qbittorrent-startup /tmp/qbittorrent-startup.go && \
    go build -o cross-seed /tmp/cross-seed.go && \
    curl -fsSL -o app/bin/qbittorrent-nox "https://github.com/userdocs/qbittorrent-nox-static/releases/download/${VERSION}/x86_64-qbittorrent-nox" && \
    curl -fsSLO --output-dir /tmp "https://github.com/openSUSE/catatonit/releases/download/${CATATONIT_VERSION}/catatonit.x86_64{,.asc}" && \
    gpg --keyserver keyserver.ubuntu.com --recv-keys 5F36C6C61B5460124A75F5A69E18AA267DDB8DB4 && \
    gpg --verify /tmp/catatonit.x86_64.asc /tmp/catatonit.x86_64 && \
    mv /tmp/catatonit.x86_64 usr/bin/catatonit && \
    chmod +x usr/bin/catatonit

FROM scratch

WORKDIR /app

ENV QBT_CONFIRM_LEGAL_NOTICE=1 \
    QBT_WEBUI_PORT=8080 \
    QBT_TORRENTING_PORT=50413 \
    HOME="/config" \
    XDG_CONFIG_HOME="/config" \
    XDG_DATA_HOME="/config" \
    SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt

COPY --from=build /rootfs /
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY qBittorrent.conf qBittorrent.conf

VOLUME /config

EXPOSE 8080

ENTRYPOINT ["catatonit", "--", "/app/qbittorrent-startup"]
