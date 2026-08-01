FROM ubuntu:26.04 AS builder

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates wget gpg apt-transport-https build-essential python3-pip meson ninja-build pkg-config git \
        liblog4cplus-dev libssl-dev libkrb5-dev libgssapi-krb5-2 libpq-dev libmysqlclient-dev libboost1.88-all-dev dpkg-dev \
        libtdb-dev libtalloc-dev libtevent-dev libgnutls28-dev libparse-yapp-perl libacl1-dev libtasn1-bin libtasn1-6-dev bison flex && \
    \
    wget --progress=dot:giga -P /tmp https://gitlab.isc.org/isc-projects/kea/-/archive/Kea-3.0.3/kea-Kea-3.0.3.tar.gz && \
    tar xzf /tmp/kea-Kea-3.0.3.tar.gz -C /tmp && \
    meson setup /tmp/kea-Kea-3.0.3/build /tmp/kea-Kea-3.0.3 -Dbuildtype=plain -Ddebug=false -Dkrb5=enabled -Dmysql=disabled -Dpostgresql=disabled -Dtests=disabled -Dfuzz=disabled -Dcpp_std=gnu++23 -Dprefix=/usr && \
    meson compile -C /tmp/kea-Kea-3.0.3/build ./src/hooks/d2/gss_tsig/ddns_gss_tsig.so:shared_library && \
    \
    wget --progress=dot:giga -P /tmp https://gitlab.com/samba-team/samba/-/archive/v4-23-stable/samba-v4-23-stable.tar.gz && \
    tar xzf /tmp/samba-v4-23-stable.tar.gz -C /tmp

WORKDIR  /tmp/samba-v4-23-stable

RUN ./configure --without-ad-dc --without-ads --without-ldap --without-ldb-lmdb --without-json --without-libarchive --without-acl-support --without-pam --disable-python --with-profiling-data --with-system-mitkrb5 --with-shared-modules='!vfs_snapper' && \
    make -j"$(nproc)"

WORKDIR /tmp/samba-v4-23-stable/source3/utils

RUN printf '%s\n' \
        '#include <stdbool.h>' \
        '#include <stdint.h>' \
        '#include <unistd.h>' \
        '' \
        'void *smbprofile_magic = NULL;' \
        'bool smbprofile_collect_tdb(void *tdb_ctx, void *profile_header_ptr) {' \
        '    return false;' \
        '}' \
        'bool smbprofile_persvc_collect_tdb(void *tdb_ctx, void *profile_header_ptr) {' \
        '    return false;' \
        '}' > profile_stub.c && \
    gcc -O2 smb_prometheus_endpoint.c profile_stub.c -o smb_prometheus_endpoint \
        -D_SAMBA_BUILD_=4 \
        -D__STDC_WANT_LIB_EXT1__=1 \
        -I../../bin/default/include \
        -I../../bin/default/source3/include \
        -I../../bin/default/source3 \
        -I../../bin/default \
        -I../../lib/replace \
        -I../../source3 \
        -I../../source3/include \
        -I../../ \
        -I../ \
        -I../../include \
        -L../../bin/default/lib/replace \
        -L../../bin/default/source3 \
        -ltdb -ltalloc -ltevent \
        -Wl,-Bstatic -levent -Wl,-Bdynamic

FROM ubuntu:26.04

ENV DEBIAN_FRONTEND=noninteractive

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

COPY --from=builder /tmp/kea-Kea-3.0.3/build/src/hooks/d2/gss_tsig/libddns_gss_tsig.so /usr/lib/x86_64-linux-gnu/kea/hooks/libddns_gss_tsig.so
COPY --from=builder /tmp/samba-v4-23-stable/source3/utils/smb_prometheus_endpoint /usr/bin/smb_prometheus_endpoint
COPY supervisord.conf /etc/supervisord.conf
COPY entrypoint.sh /entrypoint.sh
COPY entrypoint.d/ /entrypoint.d/

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates wget gpg && \
    keyring_location=/usr/share/keyrings/isc-stork-archive-keyring.gpg && \
    wget -qO- 'https://dl.cloudsmith.io/public/isc/stork/gpg.77F64EC28053D1FB.key' | gpg --dearmor > "${keyring_location}" && \
    wget -qO /etc/apt/sources.list.d/isc-stork.list 'https://dl.cloudsmith.io/public/isc/stork/config.deb.txt?distro=ubuntu&codename=resolute&component=main' && \
    chmod 644 /etc/apt/sources.list.d/isc-stork.list "${keyring_location}" && \
    apt-get remove --purge -y wget gpg && \
    \
    apt-get update && \
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
    apt-get autoremove -y && \
    apt-get clean && \
    setcap 'cap_net_admin,cap_net_raw=+ep' /usr/sbin/kea-dhcp4 && \
    setcap 'cap_dac_read_search,cap_sys_ptrace+ep' /usr/bin/stork-agent && \
    rm -rf /var/lib/apt/lists/* /etc/apt/sources.list.d/isc-stork.list "${keyring_location}" && \
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

EXPOSE 53 53/udp 67/udp 68/udp 80 88 88/udp 123/udp 135 137/udp 138/udp 139 389 389/udp 443 445 464 464/udp 636 3268 3269 5353 5353/udp 9119 9547 9922 49152-49252/tcp

ENTRYPOINT ["/entrypoint.sh"]