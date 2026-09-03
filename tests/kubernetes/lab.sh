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

log() { echo "==> $*"; }

require_tools() {
    local missing=0
    for tool in docker kubectl helm curl; do
        if ! command -v "${tool}" > /dev/null 2>&1; then
            echo "Required tool not found: ${tool}" >&2
            missing=1
        fi
    done
    [ "${missing}" -eq 0 ] || exit 1
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
        "$1"
}

cluster_ip() {
    kubectl -n "${NAMESPACE}" get service "$1" -o jsonpath='{.spec.clusterIP}'
}

cmd_up() {
    require_tools
    ensure_kind

    if "${KIND_BIN}" get clusters 2> /dev/null | grep -qx "${CLUSTER_NAME}"; then
        log "Cluster ${CLUSTER_NAME} already exists"
    else
        log "Creating kind cluster ${CLUSTER_NAME}"
        "${KIND_BIN}" create cluster --name "${CLUSTER_NAME}" --config "${SCRIPT_DIR}/kind-cluster.yaml" --wait 120s
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
}

cmd_build() {
    require_tools

    log "Building images"
    docker build --target core -t "${CORE_IMAGE}" "${REPO_ROOT}"
    docker build --target stork -t "${STORK_IMAGE}" "${REPO_ROOT}"
    docker build -f "${REPO_ROOT}/tests/linux-client/Dockerfile" -t "${CLIENT_IMAGE}" "${REPO_ROOT}"
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
    render "${SCRIPT_DIR}/network-attachments.yaml" | kubectl -n "${NAMESPACE}" apply -f -

    log "Installing chart"
    helm upgrade --install "${RELEASE}" "${REPO_ROOT}/charts/domain-in-a-box" \
        --namespace "${NAMESPACE}" \
        --values "${SCRIPT_DIR}/values.ci.yaml" \
        --wait=false

    log "Waiting for the domain controller to become ready"
    kubectl -n "${NAMESPACE}" rollout status "statefulset/${RELEASE}" --timeout=900s
}

cmd_test() {
    require_tools

    STORK_IP=$(cluster_ip "${RELEASE}-stork-server")
    export STORK_IP
    local job="${RELEASE}-test-runner"

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
            log "Test suite passed"
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
    kubectl -n "${NAMESPACE}" logs "statefulset/${RELEASE}" --tail=-1 > "${ARTIFACT_DIR}/domain-controller.log" 2>&1 || true
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
