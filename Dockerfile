FROM golang:1.26@sha256:978e0a4e3d953662577d784471db27742f543ab09eead06839a838188c73314b AS go-source

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
FROM go-source AS go-builder
RUN --mount=type=cache,target=/root/.cache/go-build \
    go build -o /out/dib-db-ctl ./cmd/dib-db-ctl && \
    go build -o /out/dib-identity-core-ctl ./cmd/dib-identity-core-ctl && \
    go build -o /out/dib-network-core-ctl ./cmd/dib-network-core-ctl && \
    go build -o /out/dib-observability-ctl ./cmd/dib-observability-ctl && \
    go build -o /out/dib-sysvol-sync-ctl ./cmd/dib-sysvol-sync-ctl && \
    go build -o /out/dib-bootstrap-ctl ./cmd/dib-bootstrap-ctl

FROM go-source AS bridge-go-builder
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /out/dib-external-dns-bridge ./cmd/dib-external-dns-bridge

FROM ubuntu:26.04@sha256:065aa8626841f16057b78b027b464cd59c4fac36c204c72dcbf48d66f5fa8a96 AS native-build-base

SHELL ["/bin/bash", "-o", "pipefail", "-c"]
ENV DEBIAN_FRONTEND=noninteractive
ARG NATIVE_BUILD_JOBS

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates wget gpg apt-transport-https build-essential python3-pip pkg-config git \
        libssl-dev libkrb5-dev libgssapi-krb5-2 dpkg-dev zlib1g-dev libevent-dev \
        libtdb-dev libtalloc-dev libtevent-dev libgnutls28-dev libparse-yapp-perl libacl1-dev libtasn1-bin libtasn1-6-dev bison flex && \
    apt-get clean && rm -rf /var/lib/apt/lists

FROM native-build-base AS samba-builder

SHELL ["/bin/bash", "-o", "pipefail", "-c"]
WORKDIR /tmp/samba-source
# Keep the pinned source commit and archive checksum updated together.
ARG SAMBA_COMMIT=b338f30ce090fba7eaaba589a69a2bbc34e5db8b
ARG SAMBA_ARCHIVE_SHA256=c14e36d05b59b01c19ea1ad9fbd4b4a01d1c02e25d85cd329980e9f73225df52

RUN wget --progress=dot:giga -O /tmp/samba-source.tar.gz "https://gitlab.com/samba-team/samba/-/archive/${SAMBA_COMMIT}/samba-${SAMBA_COMMIT}.tar.gz" && \
    echo "${SAMBA_ARCHIVE_SHA256}  /tmp/samba-source.tar.gz" | sha256sum --check - && \
    tar xzf /tmp/samba-source.tar.gz -C /tmp/samba-source --strip-components=1 && \
    ./configure --without-ad-dc --without-ads --without-ldap --without-ldb-lmdb --without-json --without-libarchive --without-acl-support --without-pam --disable-python --with-profiling-data --with-system-mitkrb5 --with-shared-modules='!vfs_snapper' && \
    make -j"${NATIVE_BUILD_JOBS:-$(nproc)}" && \
    printf '%s\n' \
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
        '}' > source3/utils/profile_stub.c && \
    gcc -O2 source3/utils/smb_prometheus_endpoint.c source3/utils/profile_stub.c -o /out-smb-prometheus \
        -D_SAMBA_BUILD_=4 \
        -D__STDC_WANT_LIB_EXT1__=1 \
        -Ibin/default/include \
        -Ibin/default/source3/include \
        -Ibin/default/source3 \
        -Ibin/default \
        -Ilib/replace \
        -Isource3 \
        -Isource3/include \
        -I. \
        -Isource3 \
        -Iinclude \
        -Lbin/default/lib/replace \
        -Lbin/default/source3 \
        -ltdb -ltalloc -ltevent \
        -Wl,-Bstatic -levent -Wl,-Bdynamic && \
    install -D /out-smb-prometheus /out/smb_prometheus_endpoint && \
    rm -rf /tmp/samba-source /tmp/samba-source.tar.gz /out-smb-prometheus

FROM ubuntu:26.04@sha256:065aa8626841f16057b78b027b464cd59c4fac36c204c72dcbf48d66f5fa8a96 AS stork-package-base

ENV DEBIAN_FRONTEND=noninteractive
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates wget gpg && \
    keyring_location=/usr/share/keyrings/isc-stork-archive-keyring.gpg && \
    wget -qO- 'https://dl.cloudsmith.io/public/isc/stork/gpg.77F64EC28053D1FB.key' | gpg --dearmor > "${keyring_location}" && \
    wget -qO /etc/apt/sources.list.d/isc-stork.list 'https://dl.cloudsmith.io/public/isc/stork/config.deb.txt?distro=ubuntu&codename=resolute&component=main' && \
    chmod 644 /etc/apt/sources.list.d/isc-stork.list "${keyring_location}" && \
    apt-get remove --purge -y wget gpg && \
    apt-get clean && rm -rf /var/lib/apt/lists

FROM stork-package-base AS core

SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        bind9 bind9-utils bind9-dnsutils chrony kea-dhcp4-server \
        samba samba-ad-dc samba-ad-provision krb5-user tdb-tools ldb-tools \
        isc-stork-agent postgresql-client supervisor iproute2 libcap2-bin openssh-server rsync && \
    apt-get autoremove -y && apt-get clean && \
    rm -rf /var/lib/apt/lists /etc/apt/sources.list.d/isc-stork.list /usr/share/keyrings/isc-stork-archive-keyring.gpg && \
    setcap 'cap_net_admin,cap_net_raw=+ep' /usr/sbin/kea-dhcp4 && \
    setcap 'cap_dac_read_search,cap_sys_ptrace+ep' /usr/bin/stork-agent && \
    mkdir -p /run/samba /run/named /run/kea /etc/stork /var/log/kea /var/log/samba/cores /run/sshd /var/empty && \
    useradd --system --no-create-home --home-dir /var/empty --shell /bin/sh dibsysvolsync && \
    random_password="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')" && \
    printf 'dibsysvolsync:%s\n' "${random_password}" | chpasswd && unset random_password && \
    chmod -R 775 /run/samba /run/named /etc/stork && chmod 750 /run/kea && chmod 700 /var/log/samba/cores && \
    chown -R root:bind /run/named && chown -R _kea:_kea /run/kea /var/log/kea && \
    rm -f /etc/samba/smb.conf /etc/bind/named.conf /etc/bind/named.conf.options \
        /etc/bind/named.conf.local /etc/bind/named.conf.root-hints /etc/kea/kea-dhcp4.conf \
        /etc/stork/agent.env /etc/chrony/chrony.conf

COPY --from=samba-builder /out/smb_prometheus_endpoint /usr/bin/smb_prometheus_endpoint
COPY --from=go-builder /out/dib-identity-core-ctl /usr/local/bin/dib-identity-core-ctl
COPY --from=go-builder /out/dib-network-core-ctl /usr/local/bin/dib-network-core-ctl
COPY --from=go-builder /out/dib-observability-ctl /usr/local/bin/dib-observability-ctl
COPY --from=go-builder /out/dib-sysvol-sync-ctl /usr/local/bin/dib-sysvol-sync-ctl
COPY --from=go-builder /out/dib-bootstrap-ctl /usr/local/bin/dib-bootstrap-ctl
COPY --from=bridge-go-builder /out/dib-external-dns-bridge /usr/local/bin/dib-external-dns-bridge
COPY supervisord.conf /etc/supervisord.conf

EXPOSE 22 53 53/udp 67/udp 68/udp 88 88/udp 123/udp 135 137/udp 138/udp 139 389 389/udp 445 464 464/udp 636 3268 3269 5353 5353/udp 8005 9119 9547 9922 49152-49252/tcp
ENTRYPOINT ["/usr/local/bin/dib-bootstrap-ctl", "start"]

FROM stork-package-base AS stork

RUN apt-get update && \
    apt-get install -y --no-install-recommends isc-stork-server isc-stork-server-hook-ldap && \
    apt-get autoremove -y && apt-get clean && \
    rm -rf /var/lib/apt/lists /etc/apt/sources.list.d/isc-stork.list /usr/share/keyrings/isc-stork-archive-keyring.gpg && \
    rm -f /etc/stork/server.env && \
    mkdir -p /etc/stork /usr/lib/stork-server/hooks /usr/share/stork/www/assets/authentication-methods && \
    chmod 775 /etc/stork /usr/share/stork/www/assets/authentication-methods && \
    chown root:stork-server /usr/share/stork/www/assets/authentication-methods

COPY --from=go-builder /out/dib-observability-ctl /usr/local/bin/dib-observability-ctl
EXPOSE 80 443
ENTRYPOINT ["/usr/local/bin/dib-observability-ctl"]
CMD ["run"]

FROM ubuntu:26.04@sha256:065aa8626841f16057b78b027b464cd59c4fac36c204c72dcbf48d66f5fa8a96 AS external-dns-bridge

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates krb5-user bind9-dnsutils && \
    apt-get clean && rm -rf /var/lib/apt/lists && \
    useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin dibdns && \
    install -d -o 10001 -g 10001 -m 0700 /run/dib-dns
ENV TMPDIR=/run/dib-dns
COPY --from=bridge-go-builder /out/dib-external-dns-bridge /usr/local/bin/dib-external-dns-bridge
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/dib-external-dns-bridge"]
CMD ["run"]
