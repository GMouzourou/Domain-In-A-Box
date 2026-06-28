#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
    echo "This script must be run as root." >&2
    exit 1
fi

# Load service-specific helpers to keep the main entrypoint readable.
. "/entrypoint.d/bind.sh"
. "/entrypoint.d/kea.sh"
. "/entrypoint.d/chrony.sh"

# Process variables
: "${DIB_REALM:?Environment variable DIB_REALM is not set}"
: "${DIB_INTERFACE:?Environment variable DIB_INTERFACE is not set}"

DIB_REALM=$(echo "${DIB_REALM}" | tr '[:lower:]' '[:upper:]')
DNS_DOMAIN=$(echo "${DIB_REALM}" | tr '[:upper:]' '[:lower:]')
CONTAINER_HOSTNAME=$(hostname | tr '[:upper:]' '[:lower:]')
SUBNET=$(ip route show dev "${DIB_INTERFACE}" | awk '/ link / {print $1; exit}')
REVERSE_ZONE=$(echo "${SUBNET}" | cut -d/ -f1 | awk -F. '{print $3"."$2"."$1".in-addr.arpa"}')
IP=$(ip addr show dev "${DIB_INTERFACE}" | awk '/inet / { split($2, a, "/"); print a[1]; exit }')
GATEWAY=$(ip route show dev "${DIB_INTERFACE}" | awk '/via/ {print $3; exit}')
INIT_DNS=FALSE
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
echo "Writing /etc/resolv.conf..."
tee /etc/resolv.conf >/dev/null <<EOF
search ${DNS_DOMAIN}
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

: "${DIB_DHCP_POOL:?Environment variable DIB_DHCP_POOL is not set}"
: "${DIB_DNS_FORWARDERS:?Environment variable DIB_DNS_FORWARDERS is not set}"

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

    # Process variables
    : "${DIB_DOMAIN:?Environment variable DIB_DOMAIN is not set}"
    : "${DIB_DOMAIN_ADMIN_PASSWORD:?Environment variable DIB_DOMAIN_ADMIN_PASSWORD is not set}"

    DIB_DOMAIN=$(echo "${DIB_DOMAIN}" | tr '[:lower:]' '[:upper:]')

    # Configure Samba
    echo "Provisioning Samba domain..."
    samba-tool domain provision --realm="${DIB_REALM}" --domain="${DIB_DOMAIN}" --server-role=dc --use-rfc2307 --dns-backend=SAMBA_INTERNAL \
        --adminpass="${DIB_DOMAIN_ADMIN_PASSWORD}" --host-name="${CONTAINER_HOSTNAME}" --host-ip="${IP}" \
        --option "bind interfaces only = yes" --option "interfaces = lo ${DIB_INTERFACE}" --option "dns forwarder = ${IP}:5353" \
        --option "rpc server dynamic port range = 49152-49252" --option "ntp signd socket directory = /var/lib/samba/ntp_signd" \
        --option "log file = /var/log/samba/%m.log" --option "max log size = 10000"

    touch "${PROVISION_SENTINEL}"

    INIT_DNS=TRUE
else
    DIB_SYNC_DOMAIN_ADMIN_PASSWORD_ON_RESTART="${DIB_SYNC_DOMAIN_ADMIN_PASSWORD_ON_RESTART:-false}"

    if [ "${DIB_SYNC_DOMAIN_ADMIN_PASSWORD_ON_RESTART}" = "true" ]; then
        if [ -z "${DIB_DOMAIN_ADMIN_PASSWORD}" ]; then
            echo "Failed to update Administrator password from latest configuration: DIB_DOMAIN_ADMIN_PASSWORD is not set or is empty"
        else
            echo "Updating Administrator password from latest configuration..."
            samba-tool user setpassword Administrator --newpassword="${DIB_DOMAIN_ADMIN_PASSWORD}" >/dev/null  
        fi
    fi
fi

# Ensure required config files exist without overwriting user-managed customizations.
dib_configure_bind9
dib_configure_kea
dib_configure_chrony

if [ "$INIT_DNS" = "FALSE" ]; then
    echo "Updating DHCP pool and DNS forwarders from latest configuration..."
    dib_update_bind_forwarders
    dib_update_kea_dhcp_pool
fi

# Configure Kerberos
echo "Copying Kerberos configuration..."
cp /var/lib/samba/private/krb5.conf /etc/krb5.conf
sed -i 's/^[[:space:]]*dns_lookup_kdc[[:space:]]*=[[:space:]]*true/ \tdns_lookup_kdc = false/' /etc/krb5.conf
sed -i "/^${DIB_REALM}[[:space:]]*=/a\\\\tkdc = ${IP}\n\\tadmin_server = ${IP}" /etc/krb5.conf
chown root:bind /etc/krb5.conf

export INIT_DNS
export DNS_DOMAIN
export CONTAINER_HOSTNAME
export IP
export SUBNET
export REVERSE_ZONE

validate_service_configs() {
    echo "Validating Samba config..."
    testparm -s >/dev/null

    echo "Validating BIND config..."
    named-checkconf /etc/bind/named.conf

    echo "Validating Kea DHCP4 config..."
    /usr/sbin/kea-dhcp4 -t /etc/kea/kea-dhcp4.conf >/dev/null

    echo "Validating Kea DHCP-DDNS config..."
    /usr/sbin/kea-dhcp-ddns -t /etc/kea/kea-dhcp-ddns.conf >/dev/null

    echo "Validating Chrony config..."
    chronyd -p -f /etc/chrony/chrony.conf >/dev/null
}

if [ "$#" -eq 0 ]; then
    validate_service_configs
    echo "Launching supervisord..."
    exec /usr/bin/supervisord -c /etc/supervisord.conf
else
    echo "Executing provided command: $*"
    exec "$@"
fi
