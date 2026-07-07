#!/bin/sh

# BIND9 configuration helpers for Domain-In-A-Box.

dib_configure_bind9() {
    echo "Configure /var/cache/bind..."
    chown root:bind /var/cache/bind
    chmod 775 /var/cache/bind

    if [ ! -f /etc/bind/named.conf ]; then
        echo "Writing /etc/bind/named.conf..."
        tee /etc/bind/named.conf >/dev/null <<EOF
include "/etc/bind/named.conf.options";
include "/etc/bind/named.conf.local";
include "/etc/bind/named.conf.root-hints";
EOF
    else
        echo "Keeping existing /etc/bind/named.conf"
    fi

    if [ ! -f /etc/bind/named.conf.options ]; then
        echo "Writing /etc/bind/named.conf.options..."
        tee /etc/bind/named.conf.options >/dev/null <<EOF
options {
    directory "/var/cache/bind";
    pid-file "/run/named/named.pid";

    allow-query { 127.0.0.1; ${SUBNET}; };
    allow-update { none; };
    allow-recursion { 127.0.0.1; ${SUBNET}; };
    allow-transfer { none; };
    forwarders { ${DIB_DNS_FORWARDERS} };
    listen-on port 5353 { ${IP}; };
    listen-on-v6 port 5353 { none; };
};

statistics-channels {
    inet 127.0.0.1 port 8053 allow { 127.0.0.1; };
};
EOF
    else
        echo "Keeping existing /etc/bind/named.conf.options"
    fi

    if [ ! -f /etc/bind/named.conf.local ]; then
        echo "Writing /etc/bind/named.conf.local..."
        tee /etc/bind/named.conf.local >/dev/null <<EOF
zone "${DNS_DOMAIN}" {
    type forward;
    forward only;
    forwarders { 127.0.0.1; };
};

zone "${REVERSE_ZONE}" {
    type forward;
    forward only;
    forwarders { 127.0.0.1; };
};
EOF
    else
        echo "Keeping existing /etc/bind/named.conf.local"
    fi

    if [ ! -f /etc/bind/named.conf.root-hints ]; then
        echo "Writing /etc/bind/named.conf.root-hints..."
        tee /etc/bind/named.conf.root-hints >/dev/null <<EOF
zone "." {
    type hint;
    file "/usr/share/dns/root.hints";
};
EOF
    else
        echo "Keeping existing /etc/bind/named.conf.root-hints"
    fi
}

dib_update_bind_forwarders() {
    if [ ! -f /etc/bind/named.conf.options ]; then
        echo "Skipping BIND forwarder update: /etc/bind/named.conf.options does not exist"
        return 0
    fi

    esc=$(printf '%s' "$DIB_DNS_FORWARDERS" | sed 's/[\/&]/\\&/g')
    sed -i -E "s/(forwarders[[:space:]]*\{)[^}]*\}([[:space:]]*;)/\1 ${esc} \}\2/" /etc/bind/named.conf.options
}
