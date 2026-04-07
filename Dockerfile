# Ubuntu 24.04 provides Samba AD DC + BIND9 DLZ packaging directly and avoids
# the Alpine/musl BIND DLZ segfault tracked in Samba Bug 15652.
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        bind9 \
        bind9-dnsutils \
        bind9-utils \
        chrony \
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
    mkdir -p /run/kea /run/named && \
    chmod 775 /run/kea /run/named && \
    chown -R root:_kea /run/kea && \
    chown -R root:bind /run/named && \
    rm /etc/samba/smb.conf

EXPOSE 53 53/udp 67/udp 68/udp 88 88/udp 123/udp 135 137/udp 138/udp 139 389 389/udp 445 464 464/udp 636 3268 3269 49152-49252/tcp

ENTRYPOINT ["/entrypoint.sh"]