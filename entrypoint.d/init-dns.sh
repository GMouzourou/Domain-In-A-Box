#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
    echo "This script must be run as root." >&2
    exit 1
fi

: "${INIT_DNS:?Environment variable INIT_DNS is not set}"

if [ "$INIT_DNS" = "FALSE" ]; then
    exit 0
fi

if [ "$INIT_DNS" != "TRUE" ]; then
    echo "Unexpected value for INIT_DNS: $INIT_DNS"
    exit 1
fi

echo "Waiting for BIND to start on port 53..."
while ! python3 -c "import socket, sys; s = socket.socket(); sys.exit(s.connect_ex(('127.0.0.1', 53)))" 2>/dev/null; do
    sleep 1
done

echo "Waiting for Samba LDAP on port 389..."
while ! python3 -c "import socket, sys; s = socket.socket(); sys.exit(s.connect_ex(('127.0.0.1', 389)))" 2>/dev/null; do
    sleep 1
done

: "${DNS_DOMAIN:?Environment variable DNS_DOMAIN is not set}"
: "${CONTAINER_HOSTNAME:?Environment variable CONTAINER_HOSTNAME is not set}"
: "${IP:?Environment variable IP is not set}"
: "${SUBNET:?Environment variable SUBNET is not set}"
: "${DIB_DOMAIN_ADMIN_PASSWORD:?Environment variable DIB_DOMAIN_ADMIN_PASSWORD is not set}"

REVERSE_ZONE=$(echo "${SUBNET}" | cut -d/ -f1 | awk -F. '{print $3"."$2"."$1".in-addr.arpa"}')

echo "Creating reverse zone ${REVERSE_ZONE}..."
ZONE_OUTPUT=$(samba-tool dns zonecreate 127.0.0.1 "${REVERSE_ZONE}" -U Administrator%"${DIB_DOMAIN_ADMIN_PASSWORD}" 2>&1)
ZONE_EXIT_CODE=$?

printf "%s\n" "$ZONE_OUTPUT" | while read -r line; do
    [ -n "$line" ] && echo "samba-tool zonecreate: $line"
done

if [ $ZONE_EXIT_CODE -ne 0 ]; then
    echo "Failed to create reverse zone or it already exists."
    exit 1
fi

echo "Reverse zone ${REVERSE_ZONE} created successfully."

PTR_RECORD=$(echo "${IP}" | awk -F. '{print $4}')

echo "Adding PTR record ${PTR_RECORD} with value ${CONTAINER_HOSTNAME}.${DNS_DOMAIN}"
PTR_OUTPUT=$(samba-tool dns add 127.0.0.1 "${REVERSE_ZONE}" "${PTR_RECORD}" PTR "${CONTAINER_HOSTNAME}.${DNS_DOMAIN}" -U Administrator%"${DIB_DOMAIN_ADMIN_PASSWORD}" 2>&1)
PTR_EXIT_CODE=$?

printf "%s\n" "$PTR_OUTPUT" | while read -r line; do
    [ -n "$line" ] && echo "samba-tool PTR: $line"
done

if [ $PTR_EXIT_CODE -ne 0 ]; then
    echo "Failed to create PTR record ${PTR_RECORD} in reverse zone ${REVERSE_ZONE}."
    exit 1
fi

echo "PTR record ${PTR_RECORD} created successfully."

if [ ${#CONTAINER_HOSTNAME} -gt 15 ]; then
    NETBIOS_NAME=$(testparm -s --parameter-name="netbios name" 2>/dev/null)
    echo "Adding ${CONTAINER_HOSTNAME} CNAME record for ${NETBIOS_NAME}.${DNS_DOMAIN} in zone ${DNS_DOMAIN}"
    CNAME_OUTPUT=$(samba-tool dns add 127.0.0.1 "${DNS_DOMAIN}" "${CONTAINER_HOSTNAME}" CNAME "${NETBIOS_NAME}.${DNS_DOMAIN}" -U Administrator%"${DIB_DOMAIN_ADMIN_PASSWORD}" 2>&1)
    CNAME_EXIT_CODE=$?

    printf "%s\n" "$CNAME_OUTPUT" | while read -r line; do
        [ -n "$line" ] && echo "samba-tool CNAME: $line"
    done

    if [ $CNAME_EXIT_CODE -ne 0 ]; then
        echo "Failed to create ${CONTAINER_HOSTNAME} CNAME record for ${NETBIOS_NAME}.${DNS_DOMAIN} in zone ${DNS_DOMAIN}."
        exit 1
    fi

    echo "CNAME record ${CONTAINER_HOSTNAME} created successfully."
fi

exit 0