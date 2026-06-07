# Ubuntu 26.04 provides Samba AD DC + BIND9 DLZ packaging directly and avoids
# the Alpine/musl BIND DLZ segfault tracked in Samba Bug 15652.
FROM ubuntu:26.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        bind9 \
        bind9-utils \
        bind9-dnsutils \
        chrony \
        kea-dhcp4-server \
        kea-dhcp-ddns-server \
        samba \
        samba-ad-dc \
        samba-ad-provision \
        tdb-tools \
        supervisor \
        ed \
        iproute2 && \
    apt-get clean && \
    setcap 'cap_net_admin,cap_net_raw=+ep' /usr/sbin/kea-dhcp4 && \
    rm -rf /var/lib/apt/lists/*

COPY supervisord.conf /etc/supervisord.conf
COPY entrypoint.sh /entrypoint.sh
COPY entrypoint.d/ /entrypoint.d/

RUN mkdir -p /run/named /run/kea /var/log/kea /var/log/samba/cores && \
    chmod -R 775 /entrypoint.sh /entrypoint.d /run/named /run/kea && \
    chmod 700 /var/log/samba/cores && \
    chown -R root:bind /run/named && \
    chown -R root:_kea /run/kea /var/log/kea && \
    rm /etc/samba/smb.conf

EXPOSE 53 53/udp 67/udp 68/udp 88 88/udp 123/udp 135 137/udp 138/udp 139 389 389/udp 445 464 464/udp 636 3268 3269 49152-49252/tcp

ENTRYPOINT ["/entrypoint.sh"]