# Ubuntu 26.04 provides Samba AD DC + BIND9 DLZ packaging directly and avoids
# the Alpine/musl BIND DLZ segfault tracked in Samba Bug 15652.
FROM ubuntu:26.04

ENV DEBIAN_FRONTEND=noninteractive

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

COPY supervisord.conf /etc/supervisord.conf
COPY entrypoint.sh /entrypoint.sh
COPY entrypoint.d/ /entrypoint.d/

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates wget gpg apt-transport-https build-essential python3-pip meson ninja-build pkg-config git \
        liblog4cplus-dev libssl-dev libkrb5-dev libgssapi-krb5-2 libpq-dev libmysqlclient-dev libboost1.88-all-dev dpkg-dev && \
    \
    wget --progress=dot:giga -P /tmp https://gitlab.isc.org/isc-projects/kea/-/archive/Kea-3.0.3/kea-Kea-3.0.3.tar.gz && \
    tar xzf /tmp/kea-Kea-3.0.3.tar.gz -C /tmp && \
    meson setup /tmp/kea-Kea-3.0.3/build /tmp/kea-Kea-3.0.3 -Dbuildtype=plain -Ddebug=false -Dkrb5=enabled -Dmysql=disabled -Dpostgresql=disabled -Dtests=disabled -Dfuzz=disabled -Dcpp_std=gnu++23 -Dprefix=/usr && \
    meson compile -C /tmp/kea-Kea-3.0.3/build ./src/hooks/d2/gss_tsig/ddns_gss_tsig.so:shared_library && \
    arch=$(dpkg-architecture -qDEB_HOST_MULTIARCH) && \
    \
    keyring_location=/usr/share/keyrings/isc-stork-archive-keyring.gpg && \
    wget -qO- 'https://dl.cloudsmith.io/public/isc/stork/gpg.77F64EC28053D1FB.key' | gpg --dearmor > "${keyring_location}" && \
    wget -qO /etc/apt/sources.list.d/isc-stork.list 'https://dl.cloudsmith.io/public/isc/stork/config.deb.txt?distro=ubuntu&codename=resolute&component=main' && \
    chmod 644 /etc/apt/sources.list.d/isc-stork.list "${keyring_location}" && \
    apt-get update && \
    \
    apt-get remove --purge -y \
        wget gpg apt-transport-https build-essential python3-pip meson ninja-build pkg-config git \
        liblog4cplus-dev libssl-dev libkrb5-dev libgssapi-krb5-2 libpq-dev libmysqlclient-dev libboost1.88-all-dev dpkg-dev && \
    \
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
        krb5-user \
        tdb-tools \
        ldb-tools \
        cron \
        isc-stork-server \
        isc-stork-server-hook-ldap \
        isc-stork-agent \
        postgresql \
        postgresql-contrib \
        supervisor \
        ed \
        iproute2 \
        libcap2-bin && \
    cp /tmp/kea-Kea-3.0.3/build/src/hooks/d2/gss_tsig/libddns_gss_tsig.so "/usr/lib/$arch/kea/hooks/libddns_gss_tsig.so" && \
    apt-get autoremove -y && \
    apt-get clean && \
    setcap 'cap_net_admin,cap_net_raw=+ep' /usr/sbin/kea-dhcp4 && \
    setcap 'cap_dac_read_search,cap_sys_ptrace+ep' /usr/bin/stork-agent && \
    rm -rf /var/lib/apt/lists/* /etc/apt/sources.list.d/isc-stork.list "${keyring_location}" /tmp/kea-Kea-3.0.3.tar.gz /tmp/kea-Kea-3.0.3 && \
    \
    mkdir -p /run/samba /run/named /run/kea /run/postgresql /etc/stork /var/log/kea /var/log/samba/cores && \
    chmod -R 775 /entrypoint.sh /entrypoint.d /run/samba /run/named /run/postgresql /etc/stork && \
    chmod 750 /run/kea && \
    chmod 700 /var/log/samba/cores && \
    chown -R root:bind /run/named && \
    chown -R _kea:_kea /run/kea /var/log/kea && \
    chown -R postgres:postgres /run/postgresql && \
    rm -f /etc/samba/smb.conf \
        /etc/bind/named.conf \
        /etc/bind/named.conf.options \
        /etc/bind/named.conf.local \
        /etc/bind/named.conf.root-hints \
        /etc/kea/kea-dhcp4.conf \
        /etc/kea/kea-dhcp-ddns.conf \
        /etc/stork/server.env \
        /etc/stork/agent.env \
        /etc/chrony/chrony.conf

EXPOSE 53 53/udp 67/udp 68/udp 88 88/udp 123/udp 135 137/udp 138/udp 139 389 389/udp 445 464 464/udp 636 3268 3269 5353 5353/udp 8080 49152-49252/tcp

ENTRYPOINT ["/entrypoint.sh"]