variable "TAG" {
  default = "latest"
}

variable "REGISTRY" {
  default = "gmouzourou"
}

variable "IMAGE_NAME" {
  default = "domain-in-a-box"
}

variable "STORK_IMAGE_NAME" {
  default = "domain-in-a-box-stork"
}

variable "BRIDGE_IMAGE_NAME" {
  default = "domain-in-a-box-external-dns-bridge"
}

variable "OUTPUT_DIR" {
  default = ""
}

variable "GHA_CACHE" {
  default = false
}

variable "SOURCE" {
  default = "https://github.com/GMouzourou/Domain-In-A-Box"
}

variable "REVISION" {
  default = ""
}

variable "NATIVE_BUILD_JOBS" {
  default = ""
}

variable "RELEASE_VERSION" {
  default = "dev"
}

variable "CORE_METADATA" {
  default = "{\"labels\":{}}"
}

variable "STORK_METADATA" {
  default = "{\"labels\":{}}"
}

variable "BRIDGE_METADATA" {
  default = "{\"labels\":{}}"
}

group "default" {
  targets = ["core", "stork", "external-dns-bridge", "test-client"]
}

group "product" {
  targets = ["core", "stork", "external-dns-bridge"]
}

target "_common" {
  context = "."
  platforms = ["linux/amd64"]
  labels = {
    "org.opencontainers.image.source" = SOURCE
    "org.opencontainers.image.revision" = REVISION
    "org.opencontainers.image.version" = RELEASE_VERSION
  }
  attest = OUTPUT_DIR == "" ? [] : ["type=provenance", "type=sbom"]
}

target "core" {
  inherits = ["_common"]
  target = "core"
  labels = jsondecode(CORE_METADATA).labels
  args = {
    NATIVE_BUILD_JOBS = NATIVE_BUILD_JOBS
  }
  tags = ["${REGISTRY}/${IMAGE_NAME}:${TAG}"]
  output = OUTPUT_DIR == "" ? [] : ["type=oci,dest=${OUTPUT_DIR}/core.oci.tar"]
  cache-from = GHA_CACHE ? ["type=gha,scope=dib-core-amd64"] : []
  cache-to = GHA_CACHE ? ["type=gha,scope=dib-core-amd64,mode=max"] : []
}

target "stork" {
  inherits = ["_common"]
  target = "stork"
  labels = jsondecode(STORK_METADATA).labels
  tags = ["ghcr.io/${REGISTRY}/${STORK_IMAGE_NAME}:${TAG}"]
  output = OUTPUT_DIR == "" ? [] : ["type=oci,dest=${OUTPUT_DIR}/stork.oci.tar"]
  cache-from = GHA_CACHE ? ["type=gha,scope=dib-stork-amd64"] : []
  cache-to = GHA_CACHE ? ["type=gha,scope=dib-stork-amd64,mode=max"] : []
}

target "external-dns-bridge" {
  inherits = ["_common"]
  target = "external-dns-bridge"
  labels = jsondecode(BRIDGE_METADATA).labels
  tags = ["ghcr.io/${REGISTRY}/${BRIDGE_IMAGE_NAME}:${TAG}"]
  output = OUTPUT_DIR == "" ? [] : ["type=oci,dest=${OUTPUT_DIR}/external-dns-bridge.oci.tar"]
  cache-from = GHA_CACHE ? ["type=gha,scope=dib-external-dns-bridge-amd64"] : []
  cache-to = GHA_CACHE ? ["type=gha,scope=dib-external-dns-bridge-amd64,mode=max"] : []
}

target "test-client" {
  inherits = ["_common"]
  dockerfile = "tests/linux-client/Dockerfile"
  tags = ["${REGISTRY}/${IMAGE_NAME}-test-client:${TAG}"]
  output = OUTPUT_DIR == "" ? [] : ["type=oci,dest=${OUTPUT_DIR}/test-client.oci.tar"]
  cache-from = GHA_CACHE ? ["type=gha,scope=dib-test-client-amd64"] : []
  cache-to = GHA_CACHE ? ["type=gha,scope=dib-test-client-amd64,mode=max"] : []
}
