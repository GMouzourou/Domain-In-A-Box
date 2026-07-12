#!/bin/sh

# Stork server/agent helpers for Domain-In-A-Box.

STORK_AGENT_REGISTER_SENTINEL=/var/lib/stork-agent/.stork-agent-registered

dib_configure_stork() {
    mkdir -p /usr/lib/stork-agent/hooks
    mkdir -p /usr/lib/stork-server/hooks

    if [ ! -f /etc/stork/server.env ]; then
        echo "Writing /etc/stork/server.env..."
        ldap_root=$(printf '%s' "${DNS_DOMAIN}" | awk -F. '{for (i=1; i<=NF; i++) { printf "%sDC=%s", (i==1 ? "" : ","), $i }}')
        db_password=$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 32)
        tee /etc/stork/server.env >/dev/null <<EOF
STORK_DATABASE_HOST=127.0.0.1
STORK_DATABASE_PORT=5432
STORK_DATABASE_NAME=stork
STORK_DATABASE_USER_NAME=stork
STORK_DATABASE_PASSWORD=${db_password}

STORK_REST_HOST=0.0.0.0
STORK_REST_PORT=8080

STORK_SERVER_HOOK_LDAP_URL=ldaps://127.0.0.1:636
STORK_SERVER_HOOK_LDAP_SKIP_SERVER_TLS_VERIFICATION=true
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

dib_register_stork_agent() {
    if [ -s "$STORK_AGENT_REGISTER_SENTINEL" ]; then
        echo "Stork agent is already registered; skipping registration."
        return 0
    fi

    echo "Waiting for Stork server on port 8080..."
    while ! python3 -c "import socket, sys; s = socket.socket(); sys.exit(s.connect_ex(('127.0.0.1', 8080)))" 2>/dev/null; do
        sleep 1
    done
    
    server_token=$(su -s /bin/sh -c "psql -d stork -tAqc \"SELECT * FROM secret;\"" postgres | grep "srvtkn" | awk -F'|' '{print $NF}')

    echo "Registering the Stork agent with the server..."
    stork-agent register \
        --server-url=http://127.0.0.1:8080 \
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
