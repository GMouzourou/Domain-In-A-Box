# Ubuntu 24.04 provides Samba AD DC + BIND9 DLZ packaging directly and avoids
# the Alpine/musl BIND DLZ segfault tracked in Samba Bug 15652.
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        bind9 \
        bind9-dnsutils \
        bind9-utils \
        dnsutils \
        ed \
        hostname \
        iproute2 \
        kea-dhcp-ddns-server \
        kea-dhcp4-server \
        netcat-openbsd \
        samba \
        samba-ad-dc \
        samba-ad-provision \
        samba-common-bin \
        supervisor && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

COPY supervisord.conf /etc/supervisord.conf
COPY entrypoint.sh /entrypoint.sh

RUN chmod 755 /entrypoint.sh && \
    mkdir -p /run/kea && \
    chmod 775 /run/kea && \
    chown -R root:_kea /run/kea && \
    rm /etc/samba/smb.conf

EXPOSE 53 53/udp 67/udp 68/udp 88 135 137 138 139 389 445 464 636 3268 3269

ENTRYPOINT ["/entrypoint.sh"]