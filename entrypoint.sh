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
INIT_DNS=FALSE

if [ -z "${IP}" ]; then
    echo "Failed to determine an IPv4 address for DIB_INTERFACE=${DIB_INTERFACE}. Check that the interface exists and is configured." >&2
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

if [ ! -f /etc/samba/smb.conf ]; then
    echo "Running first time setup..."

    # Process variables
    : "${DIB_DOMAIN:?Environment variable DIB_DOMAIN is not set}"
    : "${DIB_DOMAIN_ADMIN_PASSWORD:?Environment variable DIB_DOMAIN_ADMIN_PASSWORD is not set}"
    : "${DIB_DHCP_POOL:?Environment variable DIB_DHCP_POOL is not set}"
    : "${DIB_DNS_FORWARDERS:?Environment variable DIB_DNS_FORWARDERS is not set}"

    DIB_DOMAIN=$(echo "${DIB_DOMAIN}" | tr '[:lower:]' '[:upper:]')
    GATEWAY=$(ip route show dev "${DIB_INTERFACE}" | awk '/via/ {print $3; exit}')

    if [ -z "${SUBNET}" ] || [ -z "${GATEWAY}" ]; then
        echo "Failed to determine subnet/gateway for DIB_INTERFACE=${DIB_INTERFACE}. Ensure the selected interface has a connected subnet and a route via your LAN gateway." >&2
        exit 1
    fi

    # Configure Samba
    echo "Provisioning Samba domain..."
    samba-tool domain provision --realm="${DIB_REALM}" --domain="${DIB_DOMAIN}" --server-role=dc --use-rfc2307 --dns-backend=SAMBA_INTERNAL \
        --adminpass="${DIB_DOMAIN_ADMIN_PASSWORD}" --host-name="${CONTAINER_HOSTNAME}" --host-ip="${IP}" \
        --option "bind interfaces only = yes" --option "interfaces = lo ${DIB_INTERFACE}" --option "dns forwarder = ${IP}:5353" \
        --option "rpc server dynamic port range = 49152-49252" --option "ntp signd socket directory = /var/lib/samba/ntp_signd" \
        --option "log file = /var/log/samba/%m.log" --option "max log size = 10000"

    # Configure service-specific files.
    dib_configure_bind9
    dib_configure_kea
    dib_configure_chrony

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

    echo "Updating DHCP pool and DNS forwarders from latest configuration..."
    dib_update_kea_dhcp_pool
    dib_update_bind_forwarders
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

if [ "$#" -eq 0 ]; then
    echo "Launching supervisord..."
    exec /usr/bin/supervisord -c /etc/supervisord.conf
else
    echo "Executing provided command: $*"
    exec "$@"
fi
