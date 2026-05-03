#!/bin/sh

# Kea DHCP configuration helpers for Domain-In-A-Box.

dib_configure_kea() {
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
                "code": 6,
                "data": "${IP}"
            },
            {
                "name": "routers",
                "code": 3,
                "data": "${GATEWAY}"
            },
            {
                "name": "domain-name",
                "code": 15,
                "data": "${DNS_DOMAIN}"
            },
            {
                "name": "domain-search",
                "code": 119,
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
                        "output": "/var/log/kea/kea-dhcp4.log"
                    }
                ],
                "severity": "INFO"
            }
        ]
    }
}
EOF

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
                        "output": "/var/log/kea/kea-dhcp-ddns.log"
                    }
                ],
                "severity": "INFO"
            }
        ]
    }
}
EOF
}

dib_update_kea_dhcp_pool() {
    esc=$(printf '%s' "$DIB_DHCP_POOL" | sed 's/[\/&]/\\&/g')
    sed -i -E 's/("pool"[[:space:]]*:[[:space:]]*")[^"]*(")/\1'"$esc"'\2/' /etc/kea/kea-dhcp4.conf
}
