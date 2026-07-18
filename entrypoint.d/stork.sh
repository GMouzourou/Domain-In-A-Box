#!/bin/sh

# Stork server/agent helpers for Domain-In-A-Box.

STORK_AGENT_REGISTER_SENTINEL=/var/lib/stork-agent/.stork-agent-registered

dib_configure_stork() {
    mkdir -p /usr/lib/stork-agent/hooks
    mkdir -p /usr/lib/stork-server/hooks
    install -d -m 0775 /usr/share/stork/www/assets/authentication-methods
    chown root:stork-server /usr/share/stork/www/assets/authentication-methods

    if [ ! -f /etc/stork/server.env ]; then
        echo "Writing /etc/stork/server.env..."
        ldap_root=$(printf '%s' "${DNS_DOMAIN}" | awk -F. '{for (i=1; i<=NF; i++) { printf "%sDC=%s", (i==1 ? "" : ","), $i }}')
        db_password=$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 32)
        ldap_skip_tls_verification=true
        stork_rest_port=80
        stork_tls_block=""
        cert_file=/certs/cert.pem
        key_file=/certs/key.pem
        ca_file=/certs/ca.pem

        if [ -e "${cert_file}" ] || [ -e "${key_file}" ] || [ -e "${ca_file}" ]; then
            if [ ! -e "${cert_file}" ] || [ ! -e "${key_file}" ] || [ ! -e "${ca_file}" ]; then
                echo "Custom TLS is enabled by fixed paths. Provide /certs/cert.pem, /certs/key.pem and /certs/ca.pem together." >&2
                exit 1
            fi

            ldap_skip_tls_verification=false
            stork_rest_port=443
            stork_tls_block="STORK_REST_TLS_CERTIFICATE=${cert_file}
STORK_REST_TLS_PRIVATE_KEY=${key_file}"
        fi

        tee /etc/stork/server.env >/dev/null <<EOF
STORK_DATABASE_HOST=127.0.0.1
STORK_DATABASE_PORT=5432
STORK_DATABASE_NAME=stork
STORK_DATABASE_USER_NAME=stork
STORK_DATABASE_PASSWORD=${db_password}

STORK_REST_HOST=0.0.0.0
STORK_REST_PORT=${stork_rest_port}
${stork_tls_block}

STORK_SERVER_HOOK_LDAP_URL=ldaps://${CONTAINER_HOSTNAME}.${DNS_DOMAIN}:636
STORK_SERVER_HOOK_LDAP_SKIP_SERVER_TLS_VERIFICATION=${ldap_skip_tls_verification}
STORK_SERVER_HOOK_LDAP_BIND_USERDN=CN=Administrator,CN=Users,${ldap_root}
STORK_SERVER_HOOK_LDAP_BIND_PASSWORD=${DIB_DOMAIN_ADMIN_PASSWORD}
STORK_SERVER_HOOK_LDAP_ROOT=${ldap_root}
STORK_SERVER_HOOK_LDAP_MAP_GROUPS=true
STORK_SERVER_HOOK_LDAP_GROUP_ADMIN=Domain Admins
STORK_SERVER_HOOK_LDAP_GROUP_SUPER_ADMIN=Domain Admins
STORK_SERVER_HOOK_LDAP_GROUP_READ_ONLY=Domain Users
STORK_SERVER_HOOK_LDAP_OBJECT_CLASS_USER_ID=sAMAccountName
STORK_SERVER_HOOK_LDAP_OBJECT_CLASS_USER_UNIQUE_IDENTIFIER=objectGUID
STORK_SERVER_HOOK_LDAP_OBJECT_CLASS_GROUP=group
EOF
    fi

    if [ ! -f /etc/stork/agent.env ]; then
        echo "Configuring /etc/stork/agent.env..."
        tee /etc/stork/agent.env >/dev/null <<EOF
STORK_AGENT_HOST=127.0.0.1
STORK_AGENT_PORT=8081
EOF
    fi

    if getent group bind >/dev/null 2>&1; then
        usermod -aG bind stork-agent || true
    fi

    if getent group _kea >/dev/null 2>&1; then
        usermod -aG _kea stork-agent || true
    fi
}

dib_init_stork_agent() {
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

    if [ -s "$STORK_AGENT_REGISTER_SENTINEL" ]; then
        echo "Stork agent is already registered; skipping registration."
        return 0
    fi

    cert_file=/certs/cert.pem
    key_file=/certs/key.pem
    ca_file=/certs/ca.pem
    stork_server_scheme=http
    stork_server_port=80
    if [ -e "${cert_file}" ] && [ -e "${key_file}" ] && [ -e "${ca_file}" ]; then
        stork_server_scheme=https
        stork_server_port=443
    fi

    echo "Waiting for Stork server on port ${stork_server_port}..."
    while ! python3 -c "import socket, sys; s = socket.socket(); sys.exit(s.connect_ex(('127.0.0.1', ${stork_server_port})))" 2>/dev/null; do
        sleep 1
    done
    
    server_token=$(su -s /bin/sh -c "psql -d stork -tAqc \"SELECT * FROM secret;\"" postgres | grep "srvtkn" | awk -F'|' '{print $NF}')

    echo "Registering the Stork agent with the server..."
    stork-agent register \
        --server-url="${stork_server_scheme}://${CONTAINER_HOSTNAME}.${DNS_DOMAIN}:${stork_server_port}" \
        --server-token="${server_token}" \
        --agent-host=127.0.0.1 \
        --agent-port=8081 \
        --non-interactive

    chown -R stork-agent:root /var/lib/stork-agent/certs/ /var/lib/stork-agent/tokens/

    touch "$STORK_AGENT_REGISTER_SENTINEL"
}

dib_validate_stork_configs() {
    if [ ! -s /etc/stork/server.env ]; then
        echo "/etc/stork/server.env is missing or empty" >&2
        return 1
    fi

    if [ ! -s /etc/stork/agent.env ]; then
        echo "/etc/stork/agent.env is missing or empty" >&2
        return 1
    fi
}
