#!/usr/bin/env bash
# Ephemeral kind cluster with Multus + macvlan, used to run the integration
# suite against the Helm chart instead of docker compose. The macvlan segment
# gives the domain controller and the test client a shared L2 broadcast domain,
# which is what DHCP needs.
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "${SCRIPT_DIR}/../.." && pwd)
TOOL_DIR="${REPO_ROOT}/.tools/bin"
ARTIFACT_DIR="${ARTIFACT_DIR:-${REPO_ROOT}/artifacts/k8s}"

CLUSTER_NAME="${CLUSTER_NAME:-dib}"
NODE_NAME="${CLUSTER_NAME}-control-plane"
NAMESPACE="${NAMESPACE:-dib}"
RELEASE="${RELEASE:-dib}"

LAB_NETWORK="${LAB_NETWORK:-dib-lab}"
LAB_SUBNET="${LAB_SUBNET:-192.168.3.0/24}"
LAB_GATEWAY="${LAB_GATEWAY:-192.168.3.254}"
NODE_LAB_IP="${NODE_LAB_IP:-192.168.3.2}"

KIND_VERSION="${KIND_VERSION:-v0.27.0}"
MULTUS_VERSION="${MULTUS_VERSION:-v4.1.4}"
CNI_PLUGINS_VERSION="${CNI_PLUGINS_VERSION:-v1.6.2}"

CORE_IMAGE="${CORE_IMAGE:-domain-in-a-box:ci}"
STORK_IMAGE="${STORK_IMAGE:-domain-in-a-box-stork:ci}"
CLIENT_IMAGE="${CLIENT_IMAGE:-domain-in-a-box-test-client:ci}"

KIND_BIN="${KIND_BIN:-}"
KIND_CONFIG="${KIND_CONFIG:-${SCRIPT_DIR}/kind-cluster.yaml}"
STORK_TEST_NODE="${STORK_TEST_NODE:-}"
TLS_DIR=""

log() { echo "==> $*"; }

require_tools() {
    local missing=0
    for tool in docker kubectl helm curl openssl; do
        if ! command -v "${tool}" > /dev/null 2>&1; then
            echo "Required tool not found: ${tool}" >&2
            missing=1
        fi
    done
    [ "${missing}" -eq 0 ] || exit 1
}

cleanup_tls_material() {
    if [[ -z "${TLS_DIR}" || ! -d "${TLS_DIR}" ]]; then
        return
    fi
    rm -f -- \
        "${TLS_DIR}/ca.crt" "${TLS_DIR}/ca.key" "${TLS_DIR}/ca.srl" \
        "${TLS_DIR}/controller.crt" "${TLS_DIR}/controller.csr" "${TLS_DIR}/controller.ext" "${TLS_DIR}/controller.key" \
        "${TLS_DIR}/stork.crt" "${TLS_DIR}/stork.csr" "${TLS_DIR}/stork.ext" "${TLS_DIR}/stork.key"
    rmdir -- "${TLS_DIR}" 2>/dev/null || true
    TLS_DIR=""
}

generate_test_server_certificate() {
    local tls_dir=$1 name=$2 hostname=$3 extra_san=${4:-}
    local san="DNS:${hostname}"
    local serial_args=()
    if [[ -n "${extra_san}" ]]; then
        san="${san},${extra_san}"
    fi
    if [[ -f "${tls_dir}/ca.srl" ]]; then
        serial_args=(-CAserial "${tls_dir}/ca.srl")
    else
        serial_args=(-CAcreateserial)
    fi

    openssl req -new -newkey rsa:2048 -nodes \
        -keyout "${tls_dir}/${name}.key" \
        -out "${tls_dir}/${name}.csr" \
        -subj "/CN=${hostname}"
    printf '%s\n' \
        'basicConstraints=critical,CA:FALSE' \
        'keyUsage=critical,digitalSignature,keyEncipherment' \
        'extendedKeyUsage=serverAuth' \
        "subjectAltName=${san}" > "${tls_dir}/${name}.ext"
    openssl x509 -req -in "${tls_dir}/${name}.csr" \
        -CA "${tls_dir}/ca.crt" -CAkey "${tls_dir}/ca.key" \
        "${serial_args[@]}" \
        -out "${tls_dir}/${name}.crt" -days 2 -sha256 \
        -extfile "${tls_dir}/${name}.ext"
}

generate_test_tls_material() {
    local tls_dir=$1
    local controller_hostname="${RELEASE}-svc.${NAMESPACE}.svc.cluster.local"
    local stork_hostname="${RELEASE}-stork-server.${NAMESPACE}.svc.cluster.local"

    openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 2 \
        -keyout "${tls_dir}/ca.key" \
        -out "${tls_dir}/ca.crt" \
        -subj "/CN=Domain-In-A-Box Integration Test CA" \
        -addext "basicConstraints=critical,CA:TRUE" \
        -addext "keyUsage=critical,keyCertSign,cRLSign"
    generate_test_server_certificate "${tls_dir}" controller "${controller_hostname}" \
        "IP:192.168.3.1,DNS:${RELEASE}-0.domain.home.arpa"
    generate_test_server_certificate "${tls_dir}" stork "${stork_hostname}"
}

ensure_kind() {
    if [ -n "${KIND_BIN}" ]; then
        return
    fi
    if command -v kind > /dev/null 2>&1; then
        KIND_BIN=$(command -v kind)
        return
    fi
    log "Installing kind ${KIND_VERSION}"
    mkdir -p "${TOOL_DIR}"
    curl -fsSLo "${TOOL_DIR}/kind" "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-amd64"
    chmod +x "${TOOL_DIR}/kind"
    KIND_BIN="${TOOL_DIR}/kind"
}

# Name of the node interface attached to the lab bridge, used as macvlan master.
macvlan_master() {
    docker exec "${NODE_NAME}" ip -o -4 addr show \
        | awk -v prefix="${NODE_LAB_IP}/" 'index($4, prefix) == 1 { print $2; exit }'
}

render() {
    sed \
        -e "s|__MASTER__|${DIB_MACVLAN_MASTER:-}|g" \
        -e "s|__RELEASE__|${RELEASE}|g" \
        -e "s|__CLIENT_IMAGE__|${CLIENT_IMAGE}|g" \
        -e "s|__STORK_IP__|${STORK_IP:-}|g" \
        -e "s|__NODE_NAME__|${NODE_NAME}|g" \
        "$1"
}

cluster_ip() {
    kubectl -n "${NAMESPACE}" get service "$1" -o jsonpath='{.spec.clusterIP}'
}

wait_for_multus_runtime() {
    log "Waiting for the node runtime to load the Multus CNI configuration"
    for _ in $(seq 1 60); do
        if docker exec "${NODE_NAME}" crictl info 2>/dev/null \
            | grep -F '"Name": "multus-cni-network"' > /dev/null; then
            return 0
        fi
        sleep 1
    done
    echo "The node runtime did not load the Multus CNI configuration" >&2
    docker exec "${NODE_NAME}" crictl info >&2 || true
    return 1
}

cmd_up() {
    require_tools
    ensure_kind

    if "${KIND_BIN}" get clusters 2> /dev/null | grep -qx "${CLUSTER_NAME}"; then
        log "Cluster ${CLUSTER_NAME} already exists"
    else
        log "Creating kind cluster ${CLUSTER_NAME}"
        "${KIND_BIN}" create cluster --name "${CLUSTER_NAME}" --config "${KIND_CONFIG}" --wait 120s
    fi
    # Multi-node kind keeps this taint; the DC and DHCP client must share this node.
    if kubectl get node "${NODE_NAME}" -o jsonpath='{.spec.taints[*].key}' \
        | grep -qw 'node-role.kubernetes.io/control-plane'; then
        kubectl taint node "${NODE_NAME}" node-role.kubernetes.io/control-plane:NoSchedule-
    fi

    if ! docker network inspect "${LAB_NETWORK}" > /dev/null 2>&1; then
        log "Creating lab bridge ${LAB_NETWORK} (${LAB_SUBNET})"
        docker network create --driver bridge --subnet "${LAB_SUBNET}" --gateway "${LAB_GATEWAY}" "${LAB_NETWORK}" > /dev/null
    fi

    # A dedicated bridge keeps DHCP broadcast off the cluster's own network.
    if ! docker network inspect "${LAB_NETWORK}" -f '{{range .Containers}}{{.Name}} {{end}}' | grep -qw "${NODE_NAME}"; then
        log "Attaching ${NODE_NAME} to ${LAB_NETWORK} as ${NODE_LAB_IP}"
        docker network connect --ip "${NODE_LAB_IP}" "${LAB_NETWORK}" "${NODE_NAME}"
    fi

    # kind node images ship a reduced /opt/cni/bin without macvlan or static.
    log "Installing CNI plugins ${CNI_PLUGINS_VERSION} on ${NODE_NAME}"
    local tmp
    tmp=$(mktemp -d)
    curl -fsSL "https://github.com/containernetworking/plugins/releases/download/${CNI_PLUGINS_VERSION}/cni-plugins-linux-amd64-${CNI_PLUGINS_VERSION}.tgz" \
        | tar -xz -C "${tmp}" ./macvlan ./static
    docker cp "${tmp}/macvlan" "${NODE_NAME}:/opt/cni/bin/macvlan"
    docker cp "${tmp}/static" "${NODE_NAME}:/opt/cni/bin/static"
    docker exec "${NODE_NAME}" chmod +x /opt/cni/bin/macvlan /opt/cni/bin/static
    rm -rf "${tmp}"

    log "Installing Multus ${MULTUS_VERSION}"
    kubectl apply -f "https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/${MULTUS_VERSION}/deployments/multus-daemonset.yml"
    kubectl -n kube-system rollout status daemonset/kube-multus-ds --timeout=300s
    wait_for_multus_runtime
}

cmd_build() {
    require_tools

    log "Building images"
    (
        cd "${REPO_ROOT}"
        RELEASE_VERSION="$(cat VERSION)" docker buildx bake -f docker-bake.hcl --load \
            --set "core.tags=${CORE_IMAGE}" \
            --set "stork.tags=${STORK_IMAGE}" \
            --set "test-client.tags=${CLIENT_IMAGE}" \
            core stork test-client
    )
}

cmd_load() {
    require_tools
    ensure_kind

    log "Loading images into ${CLUSTER_NAME}"
    "${KIND_BIN}" load docker-image --name "${CLUSTER_NAME}" "${CORE_IMAGE}" "${STORK_IMAGE}" "${CLIENT_IMAGE}"
}

cmd_images() {
    cmd_build
    cmd_load
}

cmd_deploy() {
    require_tools

    DIB_MACVLAN_MASTER=$(macvlan_master)
    if [ -z "${DIB_MACVLAN_MASTER}" ]; then
        echo "Could not find the ${NODE_NAME} interface holding ${NODE_LAB_IP}" >&2
        exit 1
    fi
    export DIB_MACVLAN_MASTER
    log "Using macvlan master ${DIB_MACVLAN_MASTER}"

    kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
    TLS_DIR=$(mktemp -d)
    trap cleanup_tls_material EXIT
    generate_test_tls_material "${TLS_DIR}"
    kubectl -n "${NAMESPACE}" create secret generic controller-tls \
        --from-file=tls.crt="${TLS_DIR}/controller.crt" \
        --from-file=tls.key="${TLS_DIR}/controller.key" \
        --from-file=ca.crt="${TLS_DIR}/ca.crt" \
        --dry-run=client -o yaml | kubectl apply -f -
    kubectl -n "${NAMESPACE}" create secret generic stork-tls \
        --from-file=tls.crt="${TLS_DIR}/stork.crt" \
        --from-file=tls.key="${TLS_DIR}/stork.key" \
        --from-file=ca.crt="${TLS_DIR}/ca.crt" \
        --dry-run=client -o yaml | kubectl apply -f -
    render "${SCRIPT_DIR}/network-attachments.yaml" | kubectl -n "${NAMESPACE}" apply -f -
    kubectl -n "${NAMESPACE}" create secret generic dib-credentials \
        --from-literal=domain-admin-password='P@ssw0rd' \
        --from-literal=stork-db-password='StorkPostgresTestPassword123!' \
        --from-literal=stork-ldap-password='StorkLDAPTestPassword123!' \
        --dry-run=client -o yaml | kubectl apply -f -

    wait_for_multus_runtime
    log "Installing chart"
    helm upgrade --install "${RELEASE}" "${REPO_ROOT}/charts/domain-in-a-box" \
        --namespace "${NAMESPACE}" \
        --values "${SCRIPT_DIR}/values.ci.yaml" \
        --set fullnameOverride="${RELEASE}" \
        --set "nodeSelector.kubernetes\.io/hostname=${NODE_NAME}" \
        --set "coreImage.repository=${CORE_IMAGE%:*}" \
        --set-string "coreImage.tag=${CORE_IMAGE##*:}" \
        --set "storkImage.repository=${STORK_IMAGE%:*}" \
        --set-string "storkImage.tag=${STORK_IMAGE##*:}" \
        --wait=false

    log "Waiting for the domain controller to become ready"
    if ! kubectl -n "${NAMESPACE}" rollout status "statefulset/${RELEASE}" --timeout=900s; then
        echo "Domain controller did not become ready; collecting pod diagnostics" >&2
        kubectl -n "${NAMESPACE}" describe pod "${RELEASE}-0" || true
        kubectl -n "${NAMESPACE}" logs "${RELEASE}-0" --all-containers=true --tail=-1 || true
        kubectl -n "${NAMESPACE}" exec "${RELEASE}-0" -- \
            python3 -c '
import json, os, socket, subprocess, sys

failed = []
addresses = json.loads(subprocess.check_output(
    ["ip", "-j", "-4", "addr", "show", "dev", os.environ["DIB_INTERFACE"]]
))[0]["addr_info"]
ip = next(a["local"] for a in addresses if a["family"] == "inet" and a["scope"] == "global")
for host, port in (("127.0.0.1", 389), ("127.0.0.1", 88), (ip, 5353)):
    try:
        with socket.create_connection((host, port), timeout=1):
            pass
    except OSError:
        failed.append(f"{host}:{port}")

print(
    "Core readiness marker missing" if not os.path.isfile("/run/dib-core-ready") else
    "Readiness endpoints not accepting connections: " + ", ".join(failed)
    if failed else "Core marker and readiness endpoints are present"
)
sys.exit(bool(failed) or not os.path.isfile("/run/dib-core-ready"))
' || true
        return 1
    fi
}

cmd_test() {
    require_tools

    STORK_IP=$(cluster_ip "${RELEASE}-stork-server")
    export STORK_IP
    local job="${RELEASE}-test-runner"

    if [[ -n "${STORK_TEST_NODE}" ]]; then
        log "Pinning Stork to the controller node for same-node callback validation"
        kubectl -n "${NAMESPACE}" patch "deployment/${RELEASE}-stork-server" --type=merge \
            -p "{\"spec\":{\"template\":{\"spec\":{\"nodeSelector\":{\"kubernetes.io/hostname\":\"${NODE_NAME}\"}}}}}"
        kubectl -n "${NAMESPACE}" rollout status "deployment/${RELEASE}-stork-server" --timeout=300s
    fi

    log "Waiting separately for Stork registration and exporter readiness"
    local stork_ready=false
    for _ in $(seq 1 60); do
        if kubectl -n "${NAMESPACE}" exec "${RELEASE}-0" -- sh -ec '
            test -s /var/lib/stork-agent/.stork-agent-registered
            python3 -c "import socket; [(socket.create_connection((\"127.0.0.1\", p), timeout=1).close()) for p in (9119, 9547)]"
        '; then
            stork_ready=true
            break
        fi
        sleep 5
    done
    if [ "${stork_ready}" != true ]; then
        echo "Stork registration or exporters did not become ready" >&2
        return 1
    fi

    kubectl -n "${NAMESPACE}" delete job "${job}" --ignore-not-found --wait=true
    render "${SCRIPT_DIR}/test-runner-job.yaml" | kubectl -n "${NAMESPACE}" apply -f -

    log "Streaming test runner output"
    kubectl -n "${NAMESPACE}" wait --for=jsonpath='{.status.phase}'=Running \
        pod -l "job-name=${job}" --timeout=300s || true
    kubectl -n "${NAMESPACE}" logs -f "job/${job}" || true

    local succeeded failed
    for _ in $(seq 1 120); do
        succeeded=$(kubectl -n "${NAMESPACE}" get job "${job}" -o jsonpath='{.status.succeeded}')
        failed=$(kubectl -n "${NAMESPACE}" get job "${job}" -o jsonpath='{.status.failed}')
        if [ "${succeeded:-0}" -gt 0 ]; then
            local agent_service expected_agent_host registered_agent_host
            agent_service=$(kubectl -n "${NAMESPACE}" get service \
                -l app.kubernetes.io/component=stork-agent \
                -o jsonpath='{.items[0].metadata.name}')
            expected_agent_host="${agent_service}.${NAMESPACE}.svc.cluster.local"
            registered_agent_host=$(kubectl -n "${NAMESPACE}" exec "${RELEASE}-0" -- \
                cat /var/lib/stork-agent/.stork-agent-registered)
            if [ "${registered_agent_host}" != "${expected_agent_host}" ]; then
                echo "Stork agent registered ${registered_agent_host}, expected ${expected_agent_host}" >&2
                return 1
            fi
            kubectl -n "${NAMESPACE}" exec -i "${RELEASE}-0" -- python3 - \
                < "${SCRIPT_DIR}/stork-management-test.py"
            log "Restarting Stork server and controller to check retained agent registration"
            kubectl -n "${NAMESPACE}" rollout restart "deployment/${RELEASE}-stork-server"
            kubectl -n "${NAMESPACE}" rollout status "deployment/${RELEASE}-stork-server" --timeout=300s
            kubectl -n "${NAMESPACE}" rollout restart "statefulset/${RELEASE}"
            kubectl -n "${NAMESPACE}" rollout status "statefulset/${RELEASE}" --timeout=900s
            registered_agent_host=$(kubectl -n "${NAMESPACE}" exec "${RELEASE}-0" -- \
                cat /var/lib/stork-agent/.stork-agent-registered)
            if [ "${registered_agent_host}" != "${expected_agent_host}" ]; then
                echo "Stork agent callback changed after restart: ${registered_agent_host}" >&2
                return 1
            fi
            kubectl -n "${NAMESPACE}" exec -i "${RELEASE}-0" -- python3 - \
                < "${SCRIPT_DIR}/stork-management-test.py"
            if [[ -n "${STORK_TEST_NODE}" ]]; then
                if [[ "${STORK_TEST_NODE}" == "${NODE_NAME}" ]]; then
                    echo "STORK_TEST_NODE must differ from the controller node" >&2
                    return 1
                fi
                log "Moving Stork to ${STORK_TEST_NODE} for different-node callback validation"
                kubectl -n "${NAMESPACE}" patch "deployment/${RELEASE}-stork-server" --type=merge \
                    -p "{\"spec\":{\"template\":{\"spec\":{\"nodeSelector\":{\"kubernetes.io/hostname\":\"${STORK_TEST_NODE}\"}}}}}"
                kubectl -n "${NAMESPACE}" rollout status "deployment/${RELEASE}-stork-server" --timeout=300s
                kubectl -n "${NAMESPACE}" exec -i "${RELEASE}-0" -- python3 - \
                    < "${SCRIPT_DIR}/stork-management-test.py"
            fi
            log "Checking that Stork rejects an incorrect LDAP certificate hostname"
            kubectl -n "${NAMESPACE}" set env "deployment/${RELEASE}-stork-server" \
                "STORK_LDAP_URL=ldaps://${RELEASE}-svc.${NAMESPACE}.svc:636"
            kubectl -n "${NAMESPACE}" rollout status "deployment/${RELEASE}-stork-server" --timeout=300s
            kubectl -n "${NAMESPACE}" exec -i "${RELEASE}-0" -- \
                env STORK_TEST_EXPECT_LDAP_REJECTION=true python3 - \
                < "${SCRIPT_DIR}/stork-management-test.py"
            if ! kubectl -n "${NAMESPACE}" logs "deployment/${RELEASE}-stork-server" \
                | grep -Fq 'certificate is valid for'; then
                echo "LDAP login failed without the expected hostname verification error" >&2
                return 1
            fi
            kubectl -n "${NAMESPACE}" set env "deployment/${RELEASE}-stork-server" \
                "STORK_LDAP_URL=ldaps://${RELEASE}-svc.${NAMESPACE}.svc.cluster.local:636"

            log "Checking that Stork rejects an untrusted LDAP root"
            TLS_DIR=$(mktemp -d)
            trap cleanup_tls_material EXIT
            openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 2 \
                -keyout "${TLS_DIR}/ca.key" -out "${TLS_DIR}/ca.crt" \
                -subj "/CN=Wrong LDAP Test CA"
            kubectl -n "${NAMESPACE}" create secret generic wrong-ldap-ca \
                --from-file=ca.crt="${TLS_DIR}/ca.crt" --dry-run=client -o yaml | kubectl apply -f -
            cleanup_tls_material
            kubectl -n "${NAMESPACE}" patch "deployment/${RELEASE}-stork-server" --type=strategic \
                -p '{"spec":{"template":{"spec":{"volumes":[{"name":"ldap-ca","secret":{"secretName":"wrong-ldap-ca"}}]}}}}'
            kubectl -n "${NAMESPACE}" rollout status "deployment/${RELEASE}-stork-server" --timeout=300s
            kubectl -n "${NAMESPACE}" exec -i "${RELEASE}-0" -- \
                env STORK_TEST_EXPECT_LDAP_REJECTION=true python3 - \
                < "${SCRIPT_DIR}/stork-management-test.py"
            if ! kubectl -n "${NAMESPACE}" logs "deployment/${RELEASE}-stork-server" \
                | grep -Fq 'certificate signed by unknown authority'; then
                echo "LDAP login failed without the expected root verification error" >&2
                return 1
            fi
            kubectl -n "${NAMESPACE}" patch "deployment/${RELEASE}-stork-server" --type=strategic \
                -p '{"spec":{"template":{"spec":{"volumes":[{"name":"ldap-ca","secret":{"secretName":"controller-tls"}}]}}}}'
            kubectl -n "${NAMESPACE}" rollout status "deployment/${RELEASE}-stork-server" --timeout=300s
            kubectl -n "${NAMESPACE}" exec -i "${RELEASE}-0" -- python3 - \
                < "${SCRIPT_DIR}/stork-management-test.py"
            kubectl -n "${NAMESPACE}" delete secret wrong-ldap-ca
            local initial_restarts current_restarts recovered=false
            initial_restarts=$(kubectl -n "${NAMESPACE}" get pod "${RELEASE}-0" \
                -o jsonpath='{.status.containerStatuses[?(@.name=="domain-server")].restartCount}')
            log "Stopping BIND intentionally to exercise liveness independently of the watchdog"
            kubectl -n "${NAMESPACE}" exec "${RELEASE}-0" -- supervisorctl stop bind
            for _ in $(seq 1 60); do
                current_restarts=$(kubectl -n "${NAMESPACE}" get pod "${RELEASE}-0" \
                    -o jsonpath='{.status.containerStatuses[?(@.name=="domain-server")].restartCount}')
                if [[ "${current_restarts:-0}" -gt "${initial_restarts:-0}" ]]; then
                    recovered=true
                    break
                fi
                sleep 5
            done
            if [[ "${recovered}" != true ]]; then
                echo "Liveness did not restart the controller after BIND stopped" >&2
                return 1
            fi
            kubectl -n "${NAMESPACE}" rollout status "statefulset/${RELEASE}" --timeout=900s
            kubectl -n "${NAMESPACE}" exec -i "${RELEASE}-0" -- python3 - \
                < "${SCRIPT_DIR}/stork-management-test.py"
            log "Test suite passed"
            log "Stork agent uses the internal callback Service ${expected_agent_host} after restarts"
            return 0
        fi
        if [ "${failed:-0}" -gt 0 ]; then
            echo "Test suite failed" >&2
            return 1
        fi
        sleep 10
    done

    echo "Timed out waiting for job ${job}" >&2
    return 1
}

cmd_logs() {
    mkdir -p "${ARTIFACT_DIR}"
    kubectl get nodes -o wide > "${ARTIFACT_DIR}/nodes.txt" 2>&1 || true
    kubectl -n "${NAMESPACE}" get all -o wide > "${ARTIFACT_DIR}/resources.txt" 2>&1 || true
    kubectl -n "${NAMESPACE}" describe pods > "${ARTIFACT_DIR}/describe-pods.txt" 2>&1 || true
    kubectl -n "${NAMESPACE}" get events --sort-by=.lastTimestamp > "${ARTIFACT_DIR}/events.txt" 2>&1 || true
    kubectl -n "${NAMESPACE}" logs "${RELEASE}-0" --all-containers=true --tail=-1 > "${ARTIFACT_DIR}/domain-controller.log" 2>&1 || true
    kubectl -n "${NAMESPACE}" logs "deployment/${RELEASE}-stork-server" --tail=-1 > "${ARTIFACT_DIR}/stork-server.log" 2>&1 || true
    kubectl -n "${NAMESPACE}" logs "job/${RELEASE}-test-runner" --tail=-1 > "${ARTIFACT_DIR}/test-runner.log" 2>&1 || true
    kubectl -n kube-system logs daemonset/kube-multus-ds --tail=-1 > "${ARTIFACT_DIR}/multus.log" 2>&1 || true
    log "Diagnostics written to ${ARTIFACT_DIR}"
}

cmd_down() {
    ensure_kind
    if "${KIND_BIN}" get clusters 2> /dev/null | grep -qx "${CLUSTER_NAME}"; then
        log "Deleting cluster ${CLUSTER_NAME}"
        "${KIND_BIN}" delete cluster --name "${CLUSTER_NAME}"
    fi
    if docker network inspect "${LAB_NETWORK}" > /dev/null 2>&1; then
        log "Removing lab bridge ${LAB_NETWORK}"
        docker network rm "${LAB_NETWORK}" > /dev/null
    fi
}

usage() {
    cat << 'EOF'
Usage: lab.sh <command>

  up       Create the kind cluster, lab bridge, CNI plugins and Multus
  build    Build the core, stork and test client images
  load     Load those images into the cluster
  images   Build and load in one step
  deploy   Apply the CI network attachments and install the Helm chart
  test     Run the test runner Job in the cluster and report its result
  logs     Collect cluster diagnostics into artifacts/k8s
  down     Delete the cluster and the lab bridge
EOF
}

case "${1:-}" in
    up) cmd_up ;;
    build) cmd_build ;;
    load) cmd_load ;;
    images) cmd_images ;;
    deploy) cmd_deploy ;;
    test) cmd_test ;;
    logs) cmd_logs ;;
    down) cmd_down ;;
    *)
        usage
        exit 1
        ;;
esac
