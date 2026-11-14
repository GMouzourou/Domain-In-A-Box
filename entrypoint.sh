#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
    echo "This script must be run as root." >&2
    exit 1
fi
rm -f /run/dib-core-ready

# Process variables
: "${DIB_REALM:?Environment variable DIB_REALM is not set}"
: "${DIB_DOMAIN:?Environment variable DIB_DOMAIN is not set}"
: "${DIB_CONTROLLER_MODE:=primary}"
: "${DIB_SYNC_DOMAIN_ADMIN_PASSWORD_ON_RESTART:=false}"
: "${DIB_INTERFACE:?Environment variable DIB_INTERFACE is not set}"
: "${DIB_DHCP_POOL:?Environment variable DIB_DHCP_POOL is not set}"
: "${DIB_DNS_FORWARDERS:?Environment variable DIB_DNS_FORWARDERS is not set}"
: "${DIB_SAMBA_METRICS_PORT:=9922}"

case "${DIB_CONTROLLER_MODE}" in
    primary) ;;
    secondary)
        : "${DIB_JOIN_SERVER:?DIB_JOIN_SERVER is required for a secondary controller}"
        ;;
    *)
        echo "DIB_CONTROLLER_MODE must be primary or secondary, got ${DIB_CONTROLLER_MODE}" >&2
        exit 1
        ;;
esac
export DIB_CONTROLLER_MODE

DIB_REALM=$(echo "${DIB_REALM}" | tr '[:lower:]' '[:upper:]')
[ "${DIB_SAMBA_METRICS_ENABLED}" = "true" ] && DIB_SAMBA_METRICS_ENABLED=on || DIB_SAMBA_METRICS_ENABLED=off

CONTAINER_HOSTNAME=$(hostname | tr '[:upper:]' '[:lower:]')
if [ "${DIB_CONTROLLER_MODE}" = "secondary" ] && [ "${#CONTAINER_HOSTNAME}" -gt 15 ]; then
    echo "A secondary controller hostname must be no more than 15 characters, got ${CONTAINER_HOSTNAME}" >&2
    exit 1
fi
DNS_DOMAIN=$(echo "${DIB_REALM}" | tr '[:upper:]' '[:lower:]')
SUBNET=$(ip route show dev "${DIB_INTERFACE}" | awk '/ link / {print $1; exit}')
REVERSE_ZONE=$(echo "${SUBNET}" | cut -d/ -f1 | awk -F. '{print $3"."$2"."$1".in-addr.arpa"}')
IP=$(ip addr show dev "${DIB_INTERFACE}" | awk '/inet / { split($2, a, "/"); print a[1]; exit }')
GATEWAY=$(ip route show dev "${DIB_INTERFACE}" | awk '/via/ {print $3; exit}')
INIT_DOMAIN=FALSE
PROVISION_SENTINEL=/var/lib/samba/.dib-provisioned
BOOTSTRAP_SENTINEL=/var/lib/samba/.dib-bootstrap-complete
DIB_BOOTSTRAP_PENDING=FALSE
if [ ! -f "${BOOTSTRAP_SENTINEL}" ]; then
    DIB_BOOTSTRAP_PENDING=TRUE
fi
STORK_BOOTSTRAP_SENTINEL=/var/lib/stork-agent/.dib-bootstrap-complete
DIB_STORK_BOOTSTRAP_PENDING=FALSE
if [ -n "${STORK_SERVER_HOST:-}" ] && [ ! -f "${STORK_BOOTSTRAP_SENTINEL}" ]; then
    DIB_STORK_BOOTSTRAP_PENDING=TRUE
fi

if [ -z "${IP}" ]; then
    echo "Failed to determine an IPv4 address for DIB_INTERFACE=${DIB_INTERFACE}. Check that the interface exists and is configured." >&2
    exit 1
fi

if [ -z "${SUBNET}" ] || [ -z "${GATEWAY}" ]; then
    echo "Failed to determine subnet/gateway for DIB_INTERFACE=${DIB_INTERFACE}. Ensure the selected interface has a connected subnet and a route via your LAN gateway." >&2
    exit 1
fi

PROVISION_SENTINEL=/var/lib/samba/.dib-provisioned
CONTROLLER_MODE_SENTINEL=/var/lib/samba/.dib-controller-mode
JOIN_STARTED_SENTINEL=/var/lib/samba/.dib-secondary-join-started

# Configure resolv.conf. A new secondary must use the existing DC's DNS during
# the join; after joining, the local DNS service becomes its resolver.
UPSTREAM_NAMESERVERS=$(awk -v self="${IP}" '/^nameserver/ && $2 != self { printf "%s ", $2 }' /etc/resolv.conf)
UPSTREAM_SEARCH=$(awk '/^search/ { $1 = ""; sub(/^ /, ""); print; exit }' /etc/resolv.conf)
SEARCH_DOMAINS="${DNS_DOMAIN}"
if [ -n "${UPSTREAM_SEARCH}" ]; then
    SEARCH_DOMAINS="${DNS_DOMAIN} ${UPSTREAM_SEARCH}"
fi
DNS_BOOTSTRAP_SERVER="${IP}"
if [ "${DIB_CONTROLLER_MODE}" = "secondary" ] && [ ! -f "${PROVISION_SENTINEL}" ]; then
    DNS_BOOTSTRAP_SERVER="${DIB_JOIN_SERVER}"
fi

echo "Writing /etc/resolv.conf..."
tee /etc/resolv.conf >/dev/null <<EOF
search ${SEARCH_DOMAINS}
nameserver ${DNS_BOOTSTRAP_SERVER}
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

if [ -f "${CONTROLLER_MODE_SENTINEL}" ]; then
    RECORDED_CONTROLLER_MODE=$(cat "${CONTROLLER_MODE_SENTINEL}")
    if [ "${RECORDED_CONTROLLER_MODE}" != "${DIB_CONTROLLER_MODE}" ]; then
        echo "Controller mode changed from ${RECORDED_CONTROLLER_MODE} to ${DIB_CONTROLLER_MODE} on existing data. Use a fresh data volume to change controller identity." >&2
        exit 1
    fi
elif [ -f "${PROVISION_SENTINEL}" ] && [ "${DIB_CONTROLLER_MODE}" != "primary" ]; then
    echo "Existing Samba data has no recorded controller mode; it cannot be adopted as a secondary controller. Use a fresh data volume." >&2
    exit 1
else
    printf '%s\n' "${DIB_CONTROLLER_MODE}" > "${CONTROLLER_MODE_SENTINEL}"
fi

if [ "${DIB_CONTROLLER_MODE}" = "secondary" ] && [ ! -f "${PROVISION_SENTINEL}" ]; then
    if [ -f "${JOIN_STARTED_SENTINEL}" ] || [ -f /etc/samba/smb.conf ]; then
        echo "A previous secondary join may have left partial Samba state. Do not retry on this volume; inspect it and use a fresh volume if rebuilding." >&2
        exit 1
    fi
    touch "${JOIN_STARTED_SENTINEL}"
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
export DIB_BOOTSTRAP_PENDING
export DIB_STORK_BOOTSTRAP_PENDING

# Configure services through their ownership boundaries.
dib-identity-core-ctl configure
if [ "$INIT_DOMAIN" = "TRUE" ]; then
    touch "${PROVISION_SENTINEL}"
    rm -f "${JOIN_STARTED_SENTINEL}"
fi
dib-network-core-ctl configure
dib-observability-ctl configure
dib-sysvol-sync-ctl configure

validate_service_configs() {
    echo "Validating Samba config..."
    dib-identity-core-ctl validate

    echo "Validating network service configuration..."
    dib-network-core-ctl validate

    echo "Validating Stork environment files..."
    dib-observability-ctl validate

    echo "Validating SYSVOL synchronization configuration..."
    dib-sysvol-sync-ctl validate
}

if [ "$#" -eq 0 ]; then
    validate_service_configs
    echo "Launching supervisord..."
    exec /usr/bin/supervisord -c /etc/supervisord.conf
else
    echo "Executing provided command: $*"
    exec "$@"
fi
