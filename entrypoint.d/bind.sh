#!/bin/sh

# BIND9 configuration helpers for Domain-In-A-Box.

dib_configure_bind9() {
    echo "Configure /var/bind..."
    chmod 775 /var/cache/bind
    chown -R root:bind /var/cache/bind

    echo "Writing /etc/bind/named.conf..."
    tee /etc/bind/named.conf >/dev/null <<EOF
key "server-tsig" {
        algorithm hmac-sha256;
        secret "$TSIG_SECRET";
};

options {
        directory "/var/cache/bind";
        pid-file "/run/named/named.pid";
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

view "default-network" {
        match-clients { 127.0.0.1; ${SUBNET}; };

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

        include "/var/lib/samba/bind-dns/named.conf";
};

view "tsig-updates" {
        match-clients { key "server-tsig"; };
        include "/var/lib/samba/bind-dns/named.conf";
};
EOF
}

dib_update_bind_forwarders() {
    esc=$(printf '%s' "$DIB_DNS_FORWARDERS" | sed 's/[\/&]/\\&/g')
    sed -i -E "s/(forwarders[[:space:]]*\{)[^}]*\}([[:space:]]*;)/\1 ${esc} \}\2/" /etc/bind/named.conf
}
