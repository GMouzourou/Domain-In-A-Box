#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
    echo "This script must be run as root." >&2
    exit 1
fi

: "${CREATE_REVERSE_ZONE:?Environment variable CREATE_REVERSE_ZONE is not set}"

if [ "$CREATE_REVERSE_ZONE" = "TRUE" ]; then
    echo "Waiting for BIND to start on port 53..."
    while ! python3 -c "import socket; s = socket.socket(); s.connect(('127.0.0.1', 53))" 2>/dev/null; do
        sleep 1
    done
    
    echo "Waiting for Samba LDAP on port 389..."
    while ! python3 -c "import socket; s = socket.socket(); s.connect(('127.0.0.1', 389))" 2>/dev/null; do
        sleep 1
    done

    : "${DNS_DOMAIN:?Environment variable DNS_DOMAIN is not set}"
    : "${CONTAINER_HOSTNAME:?Environment variable CONTAINER_HOSTNAME is not set}"
    : "${IP:?Environment variable IP is not set}"
    : "${SUBNET:?Environment variable SUBNET is not set}"
    : "${DIB_DOMAIN_ADMIN_PASSWORD:?Environment variable DIB_DOMAIN_ADMIN_PASSWORD is not set}"

    REVERSE_ZONE=$(echo "${SUBNET}" | cut -d/ -f1 | awk -F. '{print $3"."$2"."$1".in-addr.arpa"}')
    
    echo "Creating reverse zone ${REVERSE_ZONE}..."
    ZONE_OUTPUT=$(samba-tool dns zonecreate "${CONTAINER_HOSTNAME}.${DNS_DOMAIN}" "${REVERSE_ZONE}" -U Administrator%"${DIB_DOMAIN_ADMIN_PASSWORD}" 2>&1)
    ZONE_EXIT_CODE=$?

    printf "%s\n" "$ZONE_OUTPUT" | while read -r line; do
        [ -n "$line" ] && echo "samba-tool zonecreate: $line"
    done

    if [ $ZONE_EXIT_CODE -eq 0 ]; then
        echo "Reverse zone ${REVERSE_ZONE} created successfully."

        PTR_RECORD=$(echo "${IP}" | awk -F. '{print $4}')

        echo "Adding PTR record ${PTR_RECORD} with value ${CONTAINER_HOSTNAME}.${DNS_DOMAIN}"
        PTR_OUTPUT=$(samba-tool dns add "${CONTAINER_HOSTNAME}.${DNS_DOMAIN}" "${REVERSE_ZONE}" "${PTR_RECORD}" PTR "${CONTAINER_HOSTNAME}.${DNS_DOMAIN}" -U Administrator%"${DIB_DOMAIN_ADMIN_PASSWORD}" 2>&1)
        PTR_EXIT_CODE=$?

        printf "%s\n" "$PTR_OUTPUT" | while read -r line; do
            [ -n "$line" ] && echo "samba-tool PTR: $line"
        done

        if [ $PTR_EXIT_CODE -eq 0 ]; then
            echo "PTR record ${PTR_RECORD} created successfully."
            exit 0
        else
            echo "Failed to create PTR record ${PTR_RECORD} in reverse zone ${REVERSE_ZONE}."
        fi
    else
        echo "Failed to create reverse zone or it already exists."
    fi
else
    exit 0
fi

exit 1
