#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
    echo "This script must be run as root." >&2
    exit 1
fi

# Process variables
: "${DIB_REALM:?Environment variable DIB_REALM is not set}"
: "${DIB_INTERFACE:?Environment variable DIB_INTERFACE is not set}"

DIB_REALM=$(echo "${DIB_REALM}" | tr '[:lower:]' '[:upper:]')
DNS_DOMAIN=$(echo "${DIB_REALM}" | tr '[:upper:]' '[:lower:]')
HOSTNAME=$(hostname | tr '[:upper:]' '[:lower:]')
IP=$(ip addr show dev "${DIB_INTERFACE}" | awk '/inet / { split($2, a, "/"); print a[1]; exit }')

# Configure resolv.conf
echo "Writing /etc/resolv.conf..."
tee /etc/resolv.conf >/dev/null <<EOF
search ${DNS_DOMAIN}
nameserver ${IP}
EOF

# Configure /etc/hosts
echo "Updating /etc/hosts..."
new_line="${IP}    ${HOSTNAME}.${DNS_DOMAIN}    ${HOSTNAME}"
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
    : "${DIB_DOMAIN_PASSWORD:?Environment variable DIB_DOMAIN_PASSWORD is not set}"
    : "${DIB_DHCP_POOL:?Environment variable DIB_DHCP_POOL is not set}"
    : "${DIB_DNS_FORWARDERS:?Environment variable DIB_DNS_FORWARDERS is not set}"

    DIB_DOMAIN=$(echo "${DIB_DOMAIN}" | tr '[:lower:]' '[:upper:]')
    SUBNET=$(ip route show dev "${DIB_INTERFACE}" | awk '/ link / {print $1; exit}')
    GATEWAY=$(ip route | awk '/default/ {print $3; exit}')
    TSIG_SECRET=$(tsig-keygen -a hmac-sha256 server-tsig | awk -F'"' '/secret/ {print $2; exit}')
    SUBNET_BASE=$(echo "${SUBNET}" | cut -d/ -f1)
    REVERSE_ZONE=$(echo "${SUBNET_BASE}" | awk -F. '{print $3"."$2"."$1".in-addr.arpa"}')
    PTR_RECORD=$(echo "${IP}" | awk -F. '{print $4}')

    # Configure Samba
    echo "Provisioning Samba domain..."
    samba-tool domain provision --use-rfc2307 --realm="${DIB_REALM}" --domain="${DIB_DOMAIN}" --server-role=dc --dns-backend=BIND9_DLZ --adminpass="${DIB_DOMAIN_PASSWORD}" --host-name="${HOSTNAME}" --host-ip="${IP}" --option "bind interfaces only = yes" --option "interfaces = lo ${DIB_INTERFACE}" --option "log file = /var/log/samba/%m.log" --option "max log size = 10000"

    # Configure BIND9
    echo "Configure /var/bind..."
    chmod 775 /var/bind
    chown -R root:bind /var/bind

    echo "Writing /etc/bind/named.conf..."
    tee /etc/bind/named.conf >/dev/null <<EOF
key "server-tsig" {
        algorithm hmac-sha256;
        secret "$TSIG_SECRET";
};

options {
        directory "/var/bind";
        pid-file "/var/run/named/named.pid";
        tkey-gssapi-keytab "/var/lib/samba/bind-dns/dns.keytab";
        
        auth-nxdomain yes;
        empty-zones-enable no;
        minimal-responses yes;
        notify no;

        allow-query { 127.0.0.1; ${SUBNET}; };
        allow-update { key "server-tsig"; };
        allow-recursion { 127.0.0.1; ${SUBNET}; };
        allow-transfer { none; };
        forwarders { ${DIB_DNS_FORWARDERS} };
        listen-on port 53 { ${IP}; 127.0.0.1; };
        listen-on-v6 { none; };
};

zone "." IN {
        type hint;
        file "/usr/share/dns/root.hints";
};

zone "localhost" IN {
        type master;
        file "/etc/bind/db.local";
};

zone "127.in-addr.arpa" IN {
        type master;
        file "/etc/bind/db.127";
};

zone "${REVERSE_ZONE}" IN {
        type master;
        file "/etc/bind/${REVERSE_ZONE}.zone";
};

include "/var/lib/samba/bind-dns/named.conf";
EOF

    echo "Writing /etc/bind/${REVERSE_ZONE}.zone..."
    tee "/etc/bind/${REVERSE_ZONE}.zone" >/dev/null <<EOF
\$TTL 86400
@   IN SOA ${HOSTNAME}.${DNS_DOMAIN}. admin.${DNS_DOMAIN}. (
        1
        3600
        1800
        604800
        86400
)
    IN NS ${HOSTNAME}.${DNS_DOMAIN}.
${PTR_RECORD} IN PTR ${HOSTNAME}.${DNS_DOMAIN}.
EOF

    # Configure Kea DHCP4
    echo "Configure /var/lib/kea..."
    chmod 775 /var/lib/kea
    chown -R root:_kea /var/lib/kea

    echo "Writing /etc/kea/kea-dhcp4.conf..."
    tee /etc/kea/kea-dhcp4.conf >/dev/null <<EOF
{
    "Dhcp4": {
        "interfaces-config": {
            "interfaces": [
                "${DIB_INTERFACE}"
            ]
        },
        "lease-database": {
            "type": "memfile"
        },
        "option-data": [
            {
                "name": "domain-name-servers",
                "data": "${IP}"
            },
            {
                "name": "routers",
                "data": "${GATEWAY}"
            },
            {
                "name": "domain-name",
                "data": "${DNS_DOMAIN}"
            },
            {
                "name": "domain-search",
                "data": "${DNS_DOMAIN}"
            }
        ],
        "subnet4": [
            {
                "id": 1,
                "subnet": "${SUBNET}",
                "pools": [
                    {
                        "pool": "${DIB_DHCP_POOL}"
                    }
                ]
            }
        ],
        "dhcp-ddns": {
            "enable-updates": true
        },
        "loggers": [
            {
                "name": "kea-dhcp4",
                "output_options": [
                    {
                        "output": "/var/log/kea-dhcp4.log"
                    }
                ],
                "severity": "INFO"
            }
        ]
    }
}
EOF

    # Configure Kea DHCP DDNS
    echo "Writing /etc/kea/kea-dhcp-ddns.conf..."
    tee /etc/kea/kea-dhcp-ddns.conf >/dev/null <<EOF
{
    "DhcpDdns": {
        "tsig-keys": [
            {
                "name": "server-tsig",
                "algorithm": "HMAC-SHA256",
                "secret": "$TSIG_SECRET"
            }
        ],
        "loggers": [
            {
                "name": "kea-dhcp-ddns",
                "output_options": [
                    {
                        "output": "/var/log/kea-dhcp-ddns.log"
                    }
                ],
                "severity": "INFO"
            }
        ]
    }
}
EOF
fi

# Configure Kerberos
echo "Copying Kerberos configuration..."
cp /var/lib/samba/private/krb5.conf /etc/krb5.conf
chgrp bind /etc/krb5.conf 2>/dev/null || true

if [ "$#" -eq 0 ]; then
    echo "Launching supervisord..."
    exec /usr/bin/supervisord -c /etc/supervisord.conf
else
    echo "Executing provided command: $*"
    exec "$@"
fi
