FROM docker.io/alpine:3.21.3 AS build

ARG QBITTORRENT_VERSION=5.0.3
ARG LIBTORRENT_VERSION=2.0.11

WORKDIR /usr/bin

RUN apk add --update --no-cache \
        ca-certificates-bundle \
        catatonit \
        tzdata \
    && wget -q "https://github.com/userdocs/qbittorrent-nox-static/releases/download/release-${QBITTORRENT_VERSION}_v${LIBTORRENT_VERSION}/x86_64-qbittorrent-nox" -O qbittorrent-nox \
    && chmod 755 ./qbittorrent-nox

FROM scratch

COPY --from=build /usr/bin /usr/bin
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo

ENV HOME=/config

VOLUME /config

WORKDIR /config

EXPOSE 8080

ENTRYPOINT ["catatonit", "--", "qbittorrent-nox"]

CMD ["--profile=/config"]
