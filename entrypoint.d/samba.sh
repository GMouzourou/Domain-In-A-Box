#!/bin/sh

# Samba configuration helpers for Domain-In-A-Box.

dib_provision_samba_domain() {
    # Configure Samba
    echo "Provisioning Samba domain..."
    : "${DIB_DOMAIN_ADMIN_PASSWORD:?Environment variable DIB_DOMAIN_ADMIN_PASSWORD is not set}"

    samba-tool domain provision --realm="${DIB_REALM}" --domain="${DIB_DOMAIN}" --server-role=dc --use-rfc2307 --dns-backend=SAMBA_INTERNAL \
        --adminpass="${DIB_DOMAIN_ADMIN_PASSWORD}" --host-name="${CONTAINER_HOSTNAME}" --host-ip="${IP}" \
        --option "bind interfaces only = yes" --option "interfaces = lo ${DIB_INTERFACE}" --option "dns forwarder = ${IP}:5353" \
        --option "rpc server dynamic port range = 49152-49252" --option "ntp signd socket directory = /var/lib/samba/ntp_signd" \
        --option "ad dc functional level = 2016" --option "log file = /var/log/samba/%m.log" --option "max log size = 10000" \
        --option "smbd profiling level = ${DIB_SAMBA_METRICS_ENABLED}"
}

dib_configure_samba() {
    # Configure Kerberos
    echo "Setting Kerberos configuration..."
    cp /var/lib/samba/private/krb5.conf /etc/krb5.conf
    sed -i 's/^[[:space:]]*dns_lookup_kdc[[:space:]]*=[[:space:]]*true/ \tdns_lookup_kdc = false/' /etc/krb5.conf
    sed -i "/^${DIB_REALM}[[:space:]]*=/a\\\\tkdc = ${IP}\n\\tadmin_server = ${IP}" /etc/krb5.conf
    chown root:bind /etc/krb5.conf

    # Configure Samba TLS assets
    cert_file=/certs/cert.pem
    key_file=/certs/key.pem
    ca_file=/certs/ca.pem

    if [ ! -e "$cert_file" ] && [ ! -e "$key_file" ] && [ ! -e "$ca_file" ]; then
        return 0
    fi

    if [ ! -e "$cert_file" ] || [ ! -e "$key_file" ] || [ ! -e "$ca_file" ]; then
        echo "Custom TLS is enabled by fixed paths. Provide /certs/cert.pem, /certs/key.pem and /certs/ca.pem together." >&2
        exit 1
    fi

    if [ ! -s "$cert_file" ] || [ ! -s "$key_file" ] || [ ! -s "$ca_file" ]; then
        echo "One or more custom Samba TLS files do not exist or are empty." >&2
        exit 1
    fi

    echo "Installing custom Samba TLS assets..."
    install -d -m 0755 /var/lib/samba/private/tls
    install -m 0644 "$cert_file" /var/lib/samba/private/tls/cert.pem
    install -m 0600 "$key_file" /var/lib/samba/private/tls/key.pem
    install -m 0644 "$ca_file" /var/lib/samba/private/tls/ca.pem
    chown root:root /var/lib/samba/private/tls/cert.pem /var/lib/samba/private/tls/key.pem /var/lib/samba/private/tls/ca.pem

    install -m 0644 "$ca_file" /usr/local/share/ca-certificates/domain-in-a-box-ldap-ca.crt
    update-ca-certificates >/dev/null 2>&1 || true
}

dib_update_samba_admin_password() {
    if [ "${DIB_SYNC_DOMAIN_ADMIN_PASSWORD_ON_RESTART}" = "true" ]; then
        if [ -z "${DIB_DOMAIN_ADMIN_PASSWORD}" ]; then
            echo "Failed to update Administrator password from latest configuration: DIB_DOMAIN_ADMIN_PASSWORD is not set or is empty"
        else
            echo "Updating Administrator password from latest configuration..."
            samba-tool user setpassword Administrator --newpassword="${DIB_DOMAIN_ADMIN_PASSWORD}" >/dev/null  
        fi
    fi
}

dib_update_samba_metrics() {
    if [ ! -f /etc/samba/smb.conf ]; then
        echo "Skipping Samba metrics update: /etc/samba/smb.conf does not exist"
        return 0
    fi

    sed -i "s|.*smbd profiling level.*|smbd profiling level = ${DIB_SAMBA_METRICS_ENABLED}|" /etc/samba/smb.conf
}