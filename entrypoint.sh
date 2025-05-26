#!/bin/sh
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
    echo "This script must be run as root." >&2
    exit 1
fi

# Process variables
: "${REALM:?Environment variable REALM is not set}"
: "${HOSTNAME:?Environment variable HOSTNAME is not set}"

REALM=$(echo "${REALM}" | tr '[:lower:]' '[:upper:]')
DNS_DOMAIN=$(echo "${REALM}" | tr '[:upper:]' '[:lower:]')
HOSTNAME=$(echo "${HOSTNAME}" | tr '[:upper:]' '[:lower:]')
IP=$(ip addr show dev eth0 | awk '/inet / { split($2, a, "/"); print a[1]; exit }')

# Configure Hostname
echo "Setting hostname to ${HOSTNAME}"
hostname "${HOSTNAME}"

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
    : "${DOMAIN:?Environment variable DOMAIN is not set}"
    : "${DOMAIN_PASSWORD:?Environment variable DOMAIN_PASSWORD is not set}"
    : "${DHCP_POOL:?Environment variable DHCP_POOL is not set}"
    : "${DNS_FORWARDERS:?Environment variable DNS_FORWARDERS is not set}"

    DOMAIN=$(echo "${DOMAIN}" | tr '[:lower:]' '[:upper:]')
    SUBNET=$(ip route show dev eth0 | awk '/ link / {print $1; exit}')
    GATEWAY=$(ip route | awk '/default/ {print $3; exit}')
    TSIG_SECRET=$(tsig-keygen -a hmac-sha256 server-tsig | awk -F'"' '/secret/ {print $2; exit}')

    # Configure Samba
    echo "Provisioning Samba domain..."
    samba-tool domain provision --use-rfc2307 --realm="${REALM}" --domain="${DOMAIN}" --server-role=dc --dns-backend=BIND9_DLZ --adminpass="${DOMAIN_PASSWORD}" --host-name="${HOSTNAME}" --host-ip="${IP}" --option "bind interfaces only = yes" --option "interfaces = lo eth0" --option "log file = /var/log/samba/%m.log" --option "max log size = 10000"

    # Configure BIND9
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
        forwarders { ${DNS_FORWARDERS} };
        listen-on port 53 { ${IP}; 127.0.0.1; };
        listen-on-v6 { none; };
};

zone "." IN {
        type hint;
        file "named.ca";
};

zone "localhost" IN {
        type master;
        file "pri/localhost.zone";
};

zone "127.in-addr.arpa" IN {
        type master;
        file "pri/127.zone";
};

include "/var/lib/samba/bind-dns/named.conf";
EOF

    # Configure Kea DHCP4
    echo "Writing /etc/kea/kea-dhcp4.conf..."
    tee /etc/kea/kea-dhcp4.conf >/dev/null <<EOF
{
    "Dhcp4": {
        "interfaces-config": {
            "interfaces": [
                "eth0"
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
                        "pool": "${DHCP_POOL}"
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
chgrp named /etc/krb5.conf

if [ "$#" -eq 0 ]; then
    echo "Launching supervisord..."
    /usr/bin/supervisord -c /etc/supervisord.conf
else
    echo "Executing provided command..."
    eval "$@"
fi
