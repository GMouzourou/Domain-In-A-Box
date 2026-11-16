#!/usr/bin/env bash
set -euo pipefail

: "${LDAP_TEST_HOST:?Set the controller FQDN present in both its certificate and LDAP SPN}"
: "${DOMAIN_CONTROLLER_IP:?}"
: "${DIB_REALM:?}"
: "${DNS_DOMAIN:?}"
: "${ADMIN_USER:?}"
: "${ADMIN_PASSWORD:?}"
: "${DIB_LDAP_CA_CERTIFICATE:?}"

workdir=$(mktemp -d)
trap 'rm -f -- "${workdir}/krb5.conf" "${workdir}/ccache" "${workdir}/result"; rmdir -- "${workdir}"' EXIT
export KRB5_CONFIG="${workdir}/krb5.conf" KRB5CCNAME="FILE:${workdir}/ccache"
export LDAPTLS_CACERT="${DIB_LDAP_CA_CERTIFICATE}" LDAPTLS_REQCERT=demand
export LDAPSASL_NOCANON=on
cat > "${KRB5_CONFIG}" <<EOF
[libdefaults]
    default_realm = ${DIB_REALM}
    rdns = false
    dns_canonicalize_hostname = false
[realms]
    ${DIB_REALM} = {
        kdc = ${DOMAIN_CONTROLLER_IP}
    }
EOF

printf '%s\n' "${ADMIN_PASSWORD}" | timeout 20 kinit "${ADMIN_USER}@${DIB_REALM}"
base_dn="DC=${DNS_DOMAIN//./,DC=}"

query() {
    timeout 20 ldapsearch -LLL -Q -Y GSSAPI "$@" -b "${base_dn}" -s base dn
}

echo "Checking encrypted LDAP/SASL"
query -O minssf=56 -H "ldap://${LDAP_TEST_HOST}" > "${workdir}/result"
grep -Fqx "dn: ${base_dn}" "${workdir}/result"

echo "Checking certificate-verified LDAPS with endpoint channel binding"
LDAPSASL_CBINDING=tls-endpoint query -O minssf=0,maxssf=0 \
    -H "ldaps://${LDAP_TEST_HOST}" > "${workdir}/result"
grep -Fqx "dn: ${base_dn}" "${workdir}/result"

echo "Checking that missing channel binding is rejected"
if LDAPSASL_CBINDING=none query -O minssf=0,maxssf=0 \
    -H "ldaps://${LDAP_TEST_HOST}" > "${workdir}/result" 2>&1; then
    echo "LDAPS GSSAPI unexpectedly accepted missing channel binding" >&2
    exit 1
fi
if ! grep -Fq '80090346' "${workdir}/result"; then
    cat "${workdir}/result" >&2
    echo "Expected a channel-binding rejection, not a connectivity or ticket failure" >&2
    exit 1
fi
echo "LDAP GSSAPI regression checks passed"
