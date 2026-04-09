# Ubuntu 24.04 provides Samba AD DC + BIND9 DLZ packaging directly and avoids
# the Alpine/musl BIND DLZ segfault tracked in Samba Bug 15652.
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        bind9 \
        bind9-utils \
        bind9-dnsutils \
        chrony \
        ed \
        iproute2 \
        kea-dhcp-ddns-server \
        kea-dhcp4-server \
        samba \
        samba-ad-dc \
        samba-ad-provision \
        supervisor && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

COPY supervisord.conf /etc/supervisord.conf
COPY entrypoint.sh /entrypoint.sh
COPY entrypoint.d/ /entrypoint.d/

RUN mkdir -p /run/kea /run/named /var/log/samba/cores && \
    chmod -R 775 /entrypoint.sh /entrypoint.d /run/kea /run/named && \
    chmod 700 /var/log/samba/cores && \
    chown -R root:_kea /run/kea && \
    chown -R root:bind /run/named && \
    rm /etc/samba/smb.conf

EXPOSE 53 53/udp 67/udp 68/udp 88 88/udp 123/udp 135 137/udp 138/udp 139 389 389/udp 445 464 464/udp 636 3268 3269 49152-49252/tcp

ENTRYPOINT ["/entrypoint.sh"]