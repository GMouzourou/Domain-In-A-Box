#!/bin/sh
set -eu

. "/entrypoint.d/stork.sh"

if [ "$(id -u)" -ne 0 ]; then
    echo "This script must be run as root." >&2
    exit 1
fi

: "${INIT_DOMAIN:?Environment variable INIT_DOMAIN is not set}"

if [ "$INIT_DOMAIN" != "TRUE" ] && [ "$INIT_DOMAIN" != "FALSE" ]; then
    echo "Unexpected value for INIT_DOMAIN: $INIT_DOMAIN"
    exit 1
fi

run_and_log() {
    info_msg=$1
    log_prefix=$2
    success_msg=$3
    fail_msg=$4
    shift 4

    echo "$info_msg"
    CMD_OUTPUT=$("$@" 2>&1)
    EXIT_CODE=$?

    if [ -n "$CMD_OUTPUT" ]; then
        printf "%s\n" "$CMD_OUTPUT" | while read -r line; do
            [ -n "$line" ] && echo "${log_prefix}: $line"
        done
    fi

    if [ $EXIT_CODE -ne 0 ]; then
        echo "$fail_msg"
        exit 1
    fi

    echo "$success_msg"
}

common_init() {
    : "${DIB_REALM:?Environment variable DIB_REALM is not set}"

    role_exists=$(su -s /bin/sh -c "psql -tAc \"SELECT 1 FROM pg_roles WHERE rolname='stork'\" postgres" postgres | tr -d '[:space:]')
    if [ "${role_exists}" != "1" ]; then
        DIB_STORK_DB_PASSWORD=$(sed -n 's/^[[:space:]]*STORK_DATABASE_PASSWORD[[:space:]]*=[[:space:]]*"\{0,1\}\([^"[:space:]]*\)"\{0,1\}[[:space:]]*$/\1/p' /etc/stork/server.env)
        pass_escaped=$(printf "%s" "${DIB_STORK_DB_PASSWORD}" | sed "s/'/''/g")
        su -s /bin/sh -c "psql -v ON_ERROR_STOP=1 -c \"CREATE ROLE \\\"stork\\\" LOGIN PASSWORD '${pass_escaped}'\" postgres" postgres
    fi

    db_exists=$(su -s /bin/sh -c "psql -tAc \"SELECT 1 FROM pg_database WHERE datname='stork'\" postgres" postgres | tr -d '[:space:]')
    if [ "${db_exists}" != "1" ]; then
        su -s /bin/sh -c "psql -v ON_ERROR_STOP=1 -c \"CREATE DATABASE \\\"stork\\\" OWNER \\\"stork\\\"\" postgres" postgres
    fi

    su -s /bin/sh -c "psql -v ON_ERROR_STOP=1 -d \"stork\" -c \"CREATE EXTENSION IF NOT EXISTS pgcrypto\" postgres" postgres

    dib_register_stork_agent
}

std_init() {
    if [ ! -s /var/lib/kea/keaddns.keytab ]; then
        echo "Keytab /var/lib/kea/keaddns.keytab is missing or empty"
        exit 1
    fi

    touch /run/kea/keaddns.ccache

    run_and_log \
        "Initialising Kerberos cache for keaddns" \
        "kinit" \
        "Kerberos cache for keaddns initialised successfully." \
        "Failed to initialise Kerberos cache for keaddns" \
        env KRB5CCNAME=FILE:/run/kea/keaddns.ccache kinit -kt /var/lib/kea/keaddns.keytab "keaddns@${DIB_REALM}"

    chown root:_kea /run/kea/keaddns.ccache
    chmod 660 /run/kea/keaddns.ccache
    CRON_LINE="0 */4 * * * KRB5CCNAME=FILE:/run/kea/keaddns.ccache /usr/bin/kinit -kt /var/lib/kea/keaddns.keytab keaddns@${DIB_REALM}"

    run_and_log \
        "Creating cron job for Kerberos cache renewal" \
        "crontab" \
        "Cron job for Kerberos cache renewal created successfully." \
        "Failed to create cron job for Kerberos cache renewal" \
        sh -c "(crontab -u _kea -l 2>/dev/null; echo \"$CRON_LINE\") | crontab -u _kea -"
}

echo "Waiting for BIND to start on port 5353..."
while ! python3 -c "import socket, sys; s = socket.socket(); sys.exit(s.connect_ex(('${IP}', 5353)))" 2>/dev/null; do
    sleep 1
done

echo "Waiting for Samba LDAP on port 389..."
while ! python3 -c "import socket, sys; s = socket.socket(); sys.exit(s.connect_ex(('127.0.0.1', 389)))" 2>/dev/null; do
    sleep 1
done

echo "Waiting for Samba KDC on port 88..."
while ! python3 -c "import socket, sys; s = socket.socket(); sys.exit(s.connect_ex(('127.0.0.1', 88)))" 2>/dev/null; do
    sleep 1
done

echo "Waiting for PostgreSQL readiness..."
while ! su -s /bin/sh -c 'pg_isready -q -d postgres' postgres 2>/dev/null; do
    sleep 1
done

common_init

if [ "$INIT_DOMAIN" = "FALSE" ]; then
    std_init
    exit 0
fi

: "${DNS_DOMAIN:?Environment variable DNS_DOMAIN is not set}"
: "${CONTAINER_HOSTNAME:?Environment variable CONTAINER_HOSTNAME is not set}"
: "${IP:?Environment variable IP is not set}"
: "${SUBNET:?Environment variable SUBNET is not set}"
: "${REVERSE_ZONE:?Environment variable REVERSE_ZONE is not set}"
: "${DIB_DOMAIN_ADMIN_PASSWORD:?Environment variable DIB_DOMAIN_ADMIN_PASSWORD is not set}"

run_and_log \
    "Raising Samba domain and forest levels..." \
    "samba-tool domain level raise" \
    "Samba domain and forest levels raised successfully." \
    "Failed to raise Samba domain and forest levels." \
    samba-tool domain level raise --domain-level=2016 --forest-level=2016

run_and_log \
    "Creating reverse zone ${REVERSE_ZONE}..." \
    "samba-tool zonecreate" \
    "Reverse zone ${REVERSE_ZONE} created successfully." \
    "Failed to create reverse zone or it already exists." \
    samba-tool dns zonecreate 127.0.0.1 "${REVERSE_ZONE}" -U Administrator%"${DIB_DOMAIN_ADMIN_PASSWORD}"

PTR_RECORD=$(echo "${IP}" | awk -F. '{print $4}')

run_and_log \
    "Adding PTR record ${PTR_RECORD} with value ${CONTAINER_HOSTNAME}.${DNS_DOMAIN}" \
    "samba-tool PTR" \
    "PTR record ${PTR_RECORD} created successfully." \
    "Failed to create PTR record ${PTR_RECORD} in reverse zone ${REVERSE_ZONE}" \
    samba-tool dns add 127.0.0.1 "${REVERSE_ZONE}" "${PTR_RECORD}" PTR "${CONTAINER_HOSTNAME}.${DNS_DOMAIN}" -U Administrator%"${DIB_DOMAIN_ADMIN_PASSWORD}"

NETBIOS_NAME=$(testparm -s --parameter-name="netbios name" 2>/dev/null)
if [ ${#CONTAINER_HOSTNAME} -gt 15 ]; then
    run_and_log \
        "Adding CNAME record ${CONTAINER_HOSTNAME} for ${NETBIOS_NAME}.${DNS_DOMAIN} in zone ${DNS_DOMAIN}" \
        "samba-tool CNAME" \
        "CNAME record ${CONTAINER_HOSTNAME} created successfully." \
        "Failed to create CNAME record ${CONTAINER_HOSTNAME} for ${NETBIOS_NAME}.${DNS_DOMAIN} in zone ${DNS_DOMAIN}" \
        samba-tool dns add 127.0.0.1 "${DNS_DOMAIN}" "${CONTAINER_HOSTNAME}" CNAME "${NETBIOS_NAME}.${DNS_DOMAIN}" -U Administrator%"${DIB_DOMAIN_ADMIN_PASSWORD}"
fi

run_and_log \
    "Creating user account for keaddns" \
    "samba-tool user" \
    "User account created successfully." \
    "Failed to create user account for keaddns" \
    samba-tool user create keaddns --description="Kea DHCP DDNS GSS-TSIG service account" --random-password

run_and_log \
    "Setting expiry for keaddns" \
    "samba-tool user setexpiry" \
    "User account expiry set successfully." \
    "Failed to set expiry for keaddns" \
    samba-tool user setexpiry keaddns --noexpiry

run_and_log \
    "Adding keaddns to DnsAdmins group" \
    "samba-tool group" \
    "keaddns added to DnsAdmins group successfully." \
    "Failed to add keaddns to DnsAdmins group" \
    samba-tool group addmembers DnsAdmins keaddns

run_and_log \
    "Exporting keytab for keaddns" \
    "samba-tool domain exportkeytab" \
    "Keytab for keaddns exported successfully." \
    "Failed to export keytab for keaddns" \
    samba-tool domain exportkeytab /var/lib/kea/keaddns.keytab --principal=keaddns@"${DIB_REALM}"

chown root:_kea /var/lib/kea/keaddns.keytab
chmod 660 /var/lib/kea/keaddns.keytab
touch /run/kea/keaddns.ccache

run_and_log \
    "Initialising Kerberos cache for keaddns" \
    "kinit" \
    "Kerberos cache for keaddns initialised successfully." \
    "Failed to initialise Kerberos cache for keaddns" \
    env KRB5CCNAME=FILE:/run/kea/keaddns.ccache kinit -kt /var/lib/kea/keaddns.keytab "keaddns@${DIB_REALM}"

chown root:_kea /run/kea/keaddns.ccache
chmod 660 /run/kea/keaddns.ccache
CRON_LINE="0 */4 * * * KRB5CCNAME=FILE:/run/kea/keaddns.ccache /usr/bin/kinit -kt /var/lib/kea/keaddns.keytab keaddns@${DIB_REALM}"

run_and_log \
    "Creating cron job for Kerberos cache renewal" \
    "crontab" \
    "Cron job for Kerberos cache renewal created successfully." \
    "Failed to create cron job for Kerberos cache renewal" \
    sh -c "(crontab -u _kea -l 2>/dev/null; echo \"$CRON_LINE\") | crontab -u _kea -"

exit 0