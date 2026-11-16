#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "${SCRIPT_DIR}/../.." && pwd)
rendered=$(mktemp)
defaults=$(mktemp)
external_defaults=$(mktemp)
disabled_dhcp=$(mktemp)
secondary=$(mktemp)
sysvol_writer=$(mktemp)
sysvol_standby=$(mktemp)
dhcp_ha_primary=$(mktemp)
dhcp_ha_standby=$(mktemp)
legacy_secondary=$(mktemp)
trap 'rm -f "${rendered}" "${defaults}" "${external_defaults}" "${disabled_dhcp}" "${secondary}" "${sysvol_writer}" "${sysvol_standby}" "${dhcp_ha_primary}" "${dhcp_ha_standby}" "${legacy_secondary}"' EXIT

helm template dib "${REPO_ROOT}/charts/domain-in-a-box" \
    --namespace dib \
    --values "${SCRIPT_DIR}/values.tls.ci.yaml" > "${rendered}"
helm template dib "${REPO_ROOT}/charts/domain-in-a-box" > "${defaults}"
helm template dib "${REPO_ROOT}/charts/domain-in-a-box" \
    --set database.external.enabled=true \
    --set database.external.passwordSecret.name=postgres-credentials \
    --set database.external.tls.rootCertSecretName=postgres-ca > "${external_defaults}"
helm template dib "${REPO_ROOT}/charts/domain-in-a-box" \
    --set domain.dhcpEnabled=false > "${disabled_dhcp}"
helm template dib-secondary "${REPO_ROOT}/charts/domain-in-a-box" \
    --namespace dib \
    --set fullnameOverride=dib-secondary \
    --set domain.controllerMode=secondary \
    --set peer.address=192.168.3.1 \
    --set peer.name=dib \
    --set multus.address=192.168.3.2/24 \
    --set-string 'nodeSelector.kubernetes\.io/hostname=worker-2' > "${secondary}"
helm template dib-secondary "${REPO_ROOT}/charts/domain-in-a-box" \
    --namespace dib \
    --set fullnameOverride=dib-secondary \
    --set domain.controllerMode=secondary \
    --set domain.joinServer=192.168.3.1 \
    --set domain.dhcpEnabled=false \
    --set storkServer.enabled=false \
    --set storkServer.host=dib-stork-server.dib.svc.cluster.local \
    --set tls.storkServer.secretName=dib-stork-server-tls \
    --set multus.networkName=dib-secondary-macvlan \
    --set multus.address=192.168.3.2/24 \
    --set-string 'nodeSelector.kubernetes\.io/hostname=worker-2' > "${legacy_secondary}"
helm template dib "${REPO_ROOT}/charts/domain-in-a-box" \
    --set sysvolSync.enabled=true \
    --set sysvolSync.secretName=dib-sysvol-writer \
    --set peer.address=192.168.3.2 \
    --set-string 'nodeSelector.kubernetes\.io/hostname=worker-1' > "${sysvol_writer}"
helm template dib-secondary "${REPO_ROOT}/charts/domain-in-a-box" \
    --namespace dib \
    --set fullnameOverride=dib-secondary \
    --set domain.controllerMode=secondary \
    --set peer.address=192.168.3.1 \
    --set peer.name=dib \
    --set multus.address=192.168.3.2/24 \
    --set-string 'nodeSelector.kubernetes\.io/hostname=worker-2' \
    --set sysvolSync.enabled=true \
    --set sysvolSync.secretName=dib-sysvol-reader > "${sysvol_standby}"
helm template dib "${REPO_ROOT}/charts/domain-in-a-box" \
    --namespace dib \
    --set dhcpHA.enabled=true \
    --set sysvolSync.enabled=true \
    --set sysvolSync.secretName=dib-sysvol-writer \
    --set peer.address=192.168.3.2 \
    --set multus.address=192.168.3.1/24 \
    --set-string 'nodeSelector.kubernetes\.io/hostname=worker-1' > "${dhcp_ha_primary}"
helm template dib-secondary "${REPO_ROOT}/charts/domain-in-a-box" \
    --namespace dib \
    --set fullnameOverride=dib-secondary \
    --set domain.controllerMode=secondary \
    --set peer.address=192.168.3.1 \
    --set peer.name=dib \
    --set multus.address=192.168.3.2/24 \
    --set dhcpHA.enabled=true \
    --set sysvolSync.enabled=true \
    --set sysvolSync.secretName=dib-sysvol-reader \
    --set-string 'nodeSelector.kubernetes\.io/hostname=worker-2' > "${dhcp_ha_standby}"

assert_env() {
    local file=$1 name=$2 value=$3
    if ! grep -A1 -F "name: ${name}" "${file}" | grep -Fq "value: \"${value}\""; then
        echo "Rendered configuration for ${name} is not ${value}" >&2
        exit 1
    fi
}

assert_env "${defaults}" STORK_SERVER_SCHEME https
if ! grep -A3 -F 'name: DIB_LDAPS_RELAY_ADDRESS' "${defaults}" | grep -Fq 'fieldPath: status.podIP'; then
    echo "Internal LDAPS relay must use the pod IP from the Downward API" >&2
    exit 1
fi
if ! awk '/- name: domain-server$/{c=1} c&&/livenessProbe:/{l=1} l&&/failureThreshold: 6/{found=1; exit} END{exit !found}' "${defaults}"; then
    echo "Domain controller container has no conservative liveness probe" >&2
    exit 1
fi
assert_env "${defaults}" DIB_DHCP_ENABLED true
assert_env "${defaults}" DIB_DHCP_HA_ENABLED false
if ! grep -Fq '"dst": "192.168.1.254/32", "gw": "192.168.1.254"' "${defaults}"; then
    echo "Multus route must preserve the primary pod-network default route" >&2
    exit 1
fi
if grep -Fq '"dst": "192.168.1.1/24"' "${defaults}"; then
    echo "Multus must not route its own connected subnet through the gateway" >&2
    exit 1
fi
assert_env "${disabled_dhcp}" DIB_DHCP_ENABLED false
assert_env "${secondary}" DIB_CONTROLLER_MODE secondary
assert_env "${secondary}" DIB_JOIN_SERVER 192.168.3.1
assert_env "${secondary}" DIB_DHCP_ENABLED false
assert_env "${secondary}" STORK_SERVER_HOST dib-stork-server.dib.svc.cluster.local
for manifest in "${secondary}" "${legacy_secondary}"; do
    assert_env "${manifest}" DIB_JOIN_SERVER 192.168.3.1
    assert_env "${manifest}" DIB_DHCP_ENABLED false
    assert_env "${manifest}" STORK_SERVER_HOST dib-stork-server.dib.svc.cluster.local
    if ! grep -Fq 'secretName: "dib-stork-server-tls"' "${manifest}" ||
        ! grep -Fq '"name": "dib-secondary-macvlan"' "${manifest}"; then
        echo "Secondary controller does not use the primary's Stork TLS Secret and its own network" >&2
        exit 1
    fi
done
if ! grep -Fq 'secretName: "dib-secondary-controller-tls"' "${secondary}"; then
    echo "Controller TLS Secret is not derived from the release name" >&2
    exit 1
fi
assert_env "${defaults}" DIB_DOMAIN DOMAIN
assert_env "${dhcp_ha_primary}" DIB_DHCP_HA_STANDBY_ADDRESS 192.168.3.2
assert_env "${dhcp_ha_standby}" DIB_DHCP_HA_PRIMARY_ADDRESS 192.168.3.1
assert_env "${dhcp_ha_standby}" DIB_DHCP_HA_STANDBY_ADDRESS 192.168.3.2
if ! grep -Fq 'kubernetes.io/hostname: worker-2' "${secondary}"; then
    echo "Secondary controller is not pinned to its requested Kubernetes host" >&2
    exit 1
fi
if grep -Fq 'type: Recreate' "${defaults}"; then
    echo "Stork without a LAN attachment should keep rolling updates" >&2
    exit 1
fi
if ! helm template dib "${REPO_ROOT}/charts/domain-in-a-box" \
    --set multus.storkServerNetworkName=dib-stork-macvlan | grep -Fq 'type: Recreate'; then
    echo "Stork with a static LAN attachment must use the Recreate strategy" >&2
    exit 1
fi
assert_env "${defaults}" STORK_SERVER_HOOK_LDAP_SKIP_SERVER_TLS_VERIFICATION false
assert_env "${external_defaults}" STORK_DATABASE_SSLMODE verify-full
assert_env "${defaults}" DIB_SYSVOL_SYNC_ROLE disabled
assert_env "${sysvol_writer}" DIB_SYSVOL_SYNC_ROLE writer
assert_env "${sysvol_writer}" DIB_SYSVOL_SYNC_PEER_ADDRESS 192.168.3.2
assert_env "${sysvol_standby}" DIB_SYSVOL_SYNC_ROLE standby
assert_env "${sysvol_standby}" DIB_SYSVOL_SYNC_WRITER_ADDRESS 192.168.3.1
assert_env "${dhcp_ha_primary}" DIB_DHCP_HA_ENABLED true
assert_env "${dhcp_ha_primary}" DIB_DHCP_HA_PRIMARY_ADDRESS 192.168.3.1
assert_env "${dhcp_ha_standby}" DIB_DHCP_HA_ENABLED true
assert_env "${dhcp_ha_standby}" DIB_DHCP_ENABLED false
if grep -Fq 'DIB_DHCP_DNS_SERVERS' "${defaults}"; then
    echo "DHCP DNS servers must default to the controller-derived list" >&2
    exit 1
fi
if ! helm template dib "${REPO_ROOT}/charts/domain-in-a-box" \
    --set-string domain.dhcpDNSServers='192.168.1.2\, 192.168.1.3' |
    grep -A1 -F 'name: DIB_DHCP_DNS_SERVERS' | grep -Fq 'value: "192.168.1.2, 192.168.1.3"'; then
    echo "domain.dhcpDNSServers is not passed to the controller" >&2
    exit 1
fi
if helm template dib "${REPO_ROOT}/charts/domain-in-a-box" \
    --set-string domain.dhcpDNSServers=dns.example.test >/dev/null 2>&1; then
    echo "domain.dhcpDNSServers must reject non-IPv4 values" >&2
    exit 1
fi
if ! grep -Fq 'containerPort: 8005' "${dhcp_ha_primary}" ||
    ! grep -Fq 'prepare-kea-ha-certificates' "${dhcp_ha_standby}"; then
    echo "DHCP HA peer security or endpoint configuration is missing" >&2
    exit 1
fi
if ! grep -Fq 'mountPath: /etc/ssh/dib-sysvol-hostkey' "${sysvol_writer}" ||
    ! grep -Fq 'mountPath: /etc/ssh/dib-sysvol-clientkey' "${sysvol_standby}"; then
    echo "SYSVOL credentials are not mounted for the appropriate controller roles" >&2
    exit 1
fi

python3 - "${defaults}" <<'PY'
import json
import os
import socket
import subprocess
import sys
from unittest.mock import patch

with open(sys.argv[1], encoding="utf-8") as manifest:
    probe_line = next(line for line in manifest if "command:" in line and "/run/dib-core-ready" in line)
probe = json.loads(probe_line.split("command:", 1)[1])[2]
addresses = json.dumps([{"addr_info": [
    {"family": "inet", "scope": "link", "local": "169.254.1.1"},
    {"family": "inet", "scope": "global", "local": "192.0.2.10"},
]}]).encode()
core = {("127.0.0.1", 389), ("127.0.0.1", 88), ("192.0.2.10", 5353)}

class FakeSocket:
    def connect_ex(self, endpoint):
        attempted.append(endpoint)
        return int(endpoint == down)

for marker, down in [(True, None), (False, None), *((True, endpoint) for endpoint in sorted(core))]:
    attempted = []
    with patch.dict(os.environ, {"DIB_INTERFACE": "net1"}), \
         patch.object(os.path, "isfile", return_value=marker), \
         patch.object(subprocess, "check_output", return_value=addresses), \
         patch.object(socket, "socket", return_value=FakeSocket()):
        try:
            exec(probe, {})
        except SystemExit as result:
            status = result.code
        else:
            raise AssertionError("readiness probe did not exit")
    assert bool(status) == (not marker or down is not None), (marker, down, status)
    assert set(attempted).issubset(core), attempted
    if marker and down is None:
        assert set(attempted) == core, attempted
PY

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

if grep -Fq 'name: dib-secondary-stork-server' "${secondary}" ||
    grep -Fq 'name: dib-secondary-postgresql' "${secondary}"; then
    echo "Secondary controller unexpectedly renders its own Stork server or PostgreSQL" >&2
    exit 1
fi

secondary_args=(
    --namespace dib
    --set fullnameOverride=dib-secondary
    --set domain.controllerMode=secondary
    --set peer.address=192.168.3.1
    --set peer.name=dib
    --set multus.address=192.168.3.2/24
    --set-string 'nodeSelector.kubernetes\.io/hostname=worker-2'
)
reject_secondary() {
    local message=$1
    shift
    if helm template dib-secondary "${REPO_ROOT}/charts/domain-in-a-box" "${secondary_args[@]}" "$@" > /dev/null 2>&1; then
        echo "${message}" >&2
        exit 1
    fi
}
reject_secondary "Chart accepted DHCP on a secondary controller" --set domain.dhcpEnabled=true
reject_secondary "Chart accepted a secondary controller without a join server" --set peer.address=
reject_secondary "Chart accepted a secondary controller without the primary's name" --set peer.name=
reject_secondary "Chart accepted a separate Stork server for the secondary controller" --set storkServer.enabled=true
reject_secondary "Chart accepted a secondary controller without host pinning" --set-string 'nodeSelector.kubernetes\.io/hostname='
reject_secondary "Chart accepted the controller's own address as its peer" --set peer.address=192.168.3.2
reject_secondary "Chart accepted a non-IPv4 peer address" --set peer.address=dib-primary

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
    --set sysvolSync.enabled=true \
    --set sysvolSync.peerAddress=192.168.3.2 > /dev/null 2>&1; then
    echo "Chart accepted enabled SYSVOL sync without a credential Secret" >&2
    exit 1
fi

if helm template dib-secondary "${REPO_ROOT}/charts/domain-in-a-box" \
    --set domain.controllerMode=secondary \
    --set domain.joinServer=192.168.3.1 \
    --set domain.dhcpEnabled=false \
    --set storkServer.enabled=false \
    --set storkServer.host=dib-stork-server.dib.svc.cluster.local \
    --set sysvolSync.enabled=true \
    --set sysvolSync.secretName=dib-sysvol-reader > /dev/null 2>&1; then
    echo "Chart accepted enabled SYSVOL sync without a writer address" >&2
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

controller_security=$(awk '/- name: domain-server$/{p=1} p&&/^          env:/{exit} p{print}' "${defaults}")
for expected in 'privileged: false' 'allowPrivilegeEscalation: true' 'drop: [ALL]' \
    '- SYS_ADMIN' '- DAC_READ_SEARCH' '- SYS_PTRACE' '- NET_ADMIN' '- NET_RAW'; do
    if ! grep -Fq -- "${expected}" <<<"${controller_security}"; then
        echo "Controller security profile is missing ${expected}" >&2
        exit 1
    fi
done
if grep -Eq 'SYS_TIME|MKNOD|AUDIT_WRITE' <<<"${controller_security}"; then
    echo "Controller unexpectedly requests unused host/device capabilities" >&2
    exit 1
fi
if ! helm template dib "${REPO_ROOT}/charts/domain-in-a-box" --set security.privileged=true \
    | grep -Fq 'privileged: true'; then
    echo "Explicit privileged compatibility mode was not rendered" >&2
    exit 1
fi
if grep -Fq 'kind: NetworkPolicy' "${defaults}"; then
    echo "NetworkPolicy must remain opt-in" >&2
    exit 1
fi
policy=$(helm template dib "${REPO_ROOT}/charts/domain-in-a-box" \
    --set networkPolicy.enabled=true --show-only templates/network-policy.yaml)
for expected in 'policyTypes: [Ingress, Egress]' 'port: 636' 'port: 8081' \
    'port: 443' 'port: 5432' 'port: 53' 'port: 123'; do
    if ! grep -Fq "${expected}" <<<"${policy}"; then
        echo "NetworkPolicy is missing ${expected}" >&2
        exit 1
    fi
done
if helm template dib "${REPO_ROOT}/charts/domain-in-a-box" \
    --set-string security.privileged=invalid >/dev/null 2>&1; then
    echo "Chart accepted a non-boolean privileged escape hatch" >&2
    exit 1
fi

echo "Helm TLS, controller security and optional NetworkPolicy checks passed"
