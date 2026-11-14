#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "${SCRIPT_DIR}/../.." && pwd)
rendered=$(mktemp)
defaults=$(mktemp)
external_defaults=$(mktemp)
trap 'rm -f "${rendered}" "${defaults}" "${external_defaults}"' EXIT

helm template dib "${REPO_ROOT}/charts/domain-in-a-box" \
    --namespace dib \
    --values "${SCRIPT_DIR}/values.tls.ci.yaml" > "${rendered}"
helm template dib "${REPO_ROOT}/charts/domain-in-a-box" > "${defaults}"
helm template dib "${REPO_ROOT}/charts/domain-in-a-box" \
    --set database.external.enabled=true \
    --set database.external.passwordSecret.name=postgres-credentials \
    --set database.external.tls.rootCertSecretName=postgres-ca > "${external_defaults}"

assert_env() {
    local file=$1 name=$2 value=$3
    if ! grep -A1 -F "name: ${name}" "${file}" | grep -Fq "value: \"${value}\""; then
        echo "Rendered configuration for ${name} is not ${value}" >&2
        exit 1
    fi
}

assert_env "${defaults}" STORK_SERVER_SCHEME https
assert_env "${defaults}" STORK_SERVER_HOOK_LDAP_SKIP_SERVER_TLS_VERIFICATION false
assert_env "${external_defaults}" STORK_DATABASE_SSLMODE verify-full

for expected in \
    'image: "postgres:18-alpine@sha256:' \
    'pg_isready' \
    'name: stork-rest' \
    'tcpSocket:' \
    'startupProbe:' \
    'readinessProbe:' \
    'livenessProbe:' \
    'memory: 1Gi' \
    'cpu: 100m'; do
    if ! grep -Fq -- "${expected}" "${defaults}"; then
        echo "Rendered chart is missing health/resource configuration: ${expected}" >&2
        exit 1
    fi
done

for expected in \
    'name: dib-stork-agent' \
    'clusterIP: None' \
    'publishNotReadyAddresses: true' \
    'port: 8081' \
    'value: "dib-stork-agent.dib.svc.cluster.local"' \
    'value: "dib-stork-server.dib.svc.cluster.local"' \
    'name: DIB_CLUSTER_DOMAIN' \
    'value: "cluster.local"' \
    'value: "https"' \
    'value: "443"' \
    'name: DIB_ALLOW_INSECURE_STORK_HTTP' \
    'value: "false"' \
    'value: "ldaps://dib-svc.dib.svc.cluster.local:636"' \
    'value: "false"' \
    'value: "verify-full"' \
    'path: cert.pem' \
    'path: key.pem' \
    'path: ca.crt'; do
    if ! grep -Fq -- "${expected}" "${rendered}"; then
        echo "Rendered chart is missing expected configuration: ${expected}" >&2
        exit 1
    fi
done

if awk '
    /^kind: Deployment$/ { in_deployment = 1 }
    /^---$/ { in_deployment = 0 }
    in_deployment && /k8s.v1.cni.cncf.io\/networks/ { found = 1 }
    END { exit !found }
' "${rendered}"; then
    echo "Stork server unexpectedly has a Multus network attachment" >&2
    exit 1
fi

if helm template dib "${REPO_ROOT}/charts/domain-in-a-box" \
    --set tls.storkServer.enabled=false > /dev/null 2>&1; then
    echo "Chart accepted HTTP Stork registration without an explicit insecure opt-in" >&2
    exit 1
fi

if helm template dib "${REPO_ROOT}/charts/domain-in-a-box" \
    --set tls.ldap.skipServerVerification=true > /dev/null 2>&1; then
    echo "Chart accepted disabled LDAP certificate verification without an explicit opt-in" >&2
    exit 1
fi

if helm template dib "${REPO_ROOT}/charts/domain-in-a-box" \
    --set tls.controller.secretName= \
    --set tls.ldap.caSecretName= \
    --set tls.ldap.skipServerVerification=false > /dev/null 2>&1; then
    echo "Chart accepted verified LDAP TLS without a CA Secret" >&2
    exit 1
fi

if helm template dib "${REPO_ROOT}/charts/domain-in-a-box" \
    --set database.external.enabled=true \
    --set database.external.passwordSecret.name=postgres-credentials \
    --set database.external.tls.sslMode=disable > /dev/null 2>&1; then
    echo "Chart accepted unencrypted external PostgreSQL without an explicit opt-in" >&2
    exit 1
fi

if helm template dib "${REPO_ROOT}/charts/domain-in-a-box" \
    --set database.external.enabled=true \
    --set database.external.passwordSecret.name=postgres-credentials \
    --set database.external.tls.sslMode=verify-full > /dev/null 2>&1; then
    echo "Chart accepted PostgreSQL verify-full without a root CA Secret" >&2
    exit 1
fi

if ! helm template dib "${REPO_ROOT}/charts/domain-in-a-box" \
    --set tls.storkServer.enabled=false \
    --set tls.storkServer.allowInsecureHttp=true \
    --set tls.ldap.skipServerVerification=true \
    --set tls.ldap.allowInsecureSkipServerVerification=true \
    --set database.external.enabled=true \
    --set database.external.passwordSecret.name=postgres-credentials \
    --set database.external.tls.sslMode=disable \
    --set database.external.tls.allowInsecure=true > /dev/null; then
    echo "Chart rejected insecure settings with explicit opt-ins" >&2
    exit 1
fi

echo "Helm secure TLS and internal Stork networking checks passed"
