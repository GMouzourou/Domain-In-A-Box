#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
    echo "This script must be run as root." >&2
    exit 1
fi

# Process variables
: "${DIB_REALM:?Environment variable DIB_REALM is not set}"
: "${DIB_DOMAIN:?Environment variable DIB_DOMAIN is not set}"
: "${DIB_SYNC_DOMAIN_ADMIN_PASSWORD_ON_RESTART:=false}"
: "${DIB_INTERFACE:?Environment variable DIB_INTERFACE is not set}"
: "${DIB_DHCP_POOL:?Environment variable DIB_DHCP_POOL is not set}"
: "${DIB_DNS_FORWARDERS:?Environment variable DIB_DNS_FORWARDERS is not set}"
: "${DIB_SAMBA_METRICS_PORT:=9922}"

DIB_REALM=$(echo "${DIB_REALM}" | tr '[:lower:]' '[:upper:]')
[ ${#DIB_DOMAIN} -gt 15 ] && : "${DIB_DOMAIN:?is longer than 15 characters}"
[ "${DIB_SAMBA_METRICS_ENABLED}" = "true" ] && DIB_SAMBA_METRICS_ENABLED=on || DIB_SAMBA_METRICS_ENABLED=off

CONTAINER_HOSTNAME=$(hostname | tr '[:upper:]' '[:lower:]')
[ ${#CONTAINER_HOSTNAME} -gt 15 ] && : "${CONTAINER_HOSTNAME:?is longer than 15 characters}"
DNS_DOMAIN=$(echo "${DIB_REALM}" | tr '[:upper:]' '[:lower:]')
SUBNET=$(ip route show dev "${DIB_INTERFACE}" | awk '/ link / {print $1; exit}')
REVERSE_ZONE=$(echo "${SUBNET}" | cut -d/ -f1 | awk -F. '{print $3"."$2"."$1".in-addr.arpa"}')
IP=$(ip addr show dev "${DIB_INTERFACE}" | awk '/inet / { split($2, a, "/"); print a[1]; exit }')
GATEWAY=$(ip route show dev "${DIB_INTERFACE}" | awk '/via/ {print $3; exit}')
INIT_DOMAIN=FALSE
PROVISION_SENTINEL=/var/lib/samba/.dib-provisioned

if [ -z "${IP}" ]; then
    echo "Failed to determine an IPv4 address for DIB_INTERFACE=${DIB_INTERFACE}. Check that the interface exists and is configured." >&2
    exit 1
fi

if [ -z "${SUBNET}" ] || [ -z "${GATEWAY}" ]; then
    echo "Failed to determine subnet/gateway for DIB_INTERFACE=${DIB_INTERFACE}. Ensure the selected interface has a connected subnet and a route via your LAN gateway." >&2
    exit 1
fi

# Configure resolv.conf
# Keep the resolver the platform handed us: pointing DNS at ourselves otherwise
# makes in-cluster service names unresolvable during bootstrap.
UPSTREAM_NAMESERVERS=$(awk -v self="${IP}" '/^nameserver/ && $2 != self { printf "%s ", $2 }' /etc/resolv.conf)
UPSTREAM_SEARCH=$(awk '/^search/ { $1 = ""; sub(/^ /, ""); print; exit }' /etc/resolv.conf)
SEARCH_DOMAINS="${DNS_DOMAIN}"
if [ -n "${UPSTREAM_SEARCH}" ]; then
    SEARCH_DOMAINS="${DNS_DOMAIN} ${UPSTREAM_SEARCH}"
fi

echo "Writing /etc/resolv.conf..."
tee /etc/resolv.conf >/dev/null <<EOF
search ${SEARCH_DOMAINS}
nameserver ${IP}
EOF

# Configure /etc/hosts
echo "Updating /etc/hosts..."
new_line="${IP}    ${CONTAINER_HOSTNAME}.${DNS_DOMAIN}    ${CONTAINER_HOSTNAME}"
ed -s /etc/hosts <<EOF
g/^${IP}/d
\$a
${new_line}
.
wq
EOF

# Keep Samba config and state on the same persistent volume without using subPath.
mkdir -p /var/lib/samba/etc-samba
if [ ! -L /etc/samba ]; then
    rm -rf /etc/samba
    ln -s /var/lib/samba/etc-samba /etc/samba
fi

if [ -f "${PROVISION_SENTINEL}" ] && [ ! -f /etc/samba/smb.conf ]; then
    echo "Samba state exists (${PROVISION_SENTINEL}) but /etc/samba/smb.conf is missing. Restore /var/lib/samba from backup or remove Samba state to re-provision." >&2
    exit 1
fi

if [ ! -f "${PROVISION_SENTINEL}" ]; then
    echo "Running first time setup..."

    # Create necessary log directories and files
    mkdir -p /var/log/kea /var/log/samba/cores
    touch /var/log/kea/kea-dhcp4.log
    chown -R root:_kea /var/log/kea
    chmod -R 664 /var/log/kea
    chmod 700 /var/log/samba/cores

    INIT_DOMAIN=TRUE
fi

export INIT_DOMAIN
export DNS_DOMAIN
export CONTAINER_HOSTNAME
export IP
export SUBNET
export REVERSE_ZONE
export GATEWAY
export DIB_UPSTREAM_NAMESERVERS="${UPSTREAM_NAMESERVERS}"

# Configure services through their ownership boundaries.
dib-identity-core-ctl configure
if [ "$INIT_DOMAIN" = "TRUE" ]; then
    touch "${PROVISION_SENTINEL}"
fi
dib-network-core-ctl configure
dib-observability-ctl configure

validate_service_configs() {
    echo "Validating Samba config..."
    dib-identity-core-ctl validate

    echo "Validating network service configuration..."
    dib-network-core-ctl validate

    echo "Validating Stork environment files..."
    dib-observability-ctl validate
}

if [ "$#" -eq 0 ]; then
    validate_service_configs
    echo "Launching supervisord..."
    exec /usr/bin/supervisord -c /etc/supervisord.conf
else
    echo "Executing provided command: $*"
    exec "$@"
fi
