# Development

Contributions are welcome. Open an issue to discuss significant changes
before starting work.

## Repository layout

| Path | Contents |
| --- | --- |
| `Dockerfile`, `docker-bake.hcl` | Shared image graph: controller, Stork, optional ExternalDNS bridge and test client. |
| `cmd/`, `internal/` | Go service supervisors and control tools. `dib-bootstrap-ctl start` is the controller entrypoint: interface discovery, provisioning or joining, and identity checks. |
| `charts/domain-in-a-box/` | Helm chart. |
| `charts/domain-in-a-box-external-dns-bridge/` | Separate opt-in application DNS chart. |
| `docker-compose.yml`, `.env.example` | Docker Compose deployment. |
| `tests/` | Integration test runner, Linux test client and Kubernetes test environment. |
| `docs/` | This manual. |

## Building

All Go modules and builders require Go 1.27.4 or newer. CI reads the version
from the module files; the controller and Linux test-client Dockerfiles use
the matching digest-pinned Go builder rather than Ubuntu's Go package.

```bash
make build         # controller image
make build-stork   # Stork image
make build-bridge  # optional bridge; no native controller compilation
make help          # all targets
```

Local Make builds default to the development tag `latest`; override it with
`VERSION=<tag>` when needed. This Make variable is not the release-version
source. Production Compose/Helm defaults use pinned release tags, and the test
stack uses its separate `:ci` tags.

Make and CI use the same explicit Bake file; include `-f docker-bake.hcl` in
direct Bake commands so it does not also read the deployment Compose file.
Kea uses Ubuntu's `kea-dhcp4-server` and `kea-common` packages, including its
HA and lease-command hooks. DHCP DNS updates use `nsupdate -g`, so the
source-built Kea GSS-TSIG hook is no longer needed. The Samba metrics endpoint
still has a cached native stage because Ubuntu does not package it.
Package installation is cached before application copies, and the bridge compiles only its Go tool.
Set `NATIVE_BUILD_JOBS` to limit native compilation on a shared development host.

CI builds named OCI archives once, with SBOM and provenance, and uses distinct
GitHub Actions cache scopes for each image. Test jobs enable Docker's containerd
image store and load the original index using `.github/scripts/oci_archive.py`; converting
through a Docker-daemon export can change configuration identity and is not used.
Publishing copies the tested archives with all manifests and preserved digests,
then verifies each destination index. It does not rebuild the product.

## Testing

| Command | What it does |
| --- | --- |
| `make test-unit` | Go unit tests for the product and the test runner. |
| `make test-all` | Builds the images and runs the Docker integration suite: health, DNS, DHCP, AD, and a Linux client domain join. |
| `make test-dns`, `make test-dhcp`, `make test-ad`, `make test-ad-linux` | Individual suites against a running test stack. |
| `make k8s-all` | Creates a kind cluster with Multus and macvlan, deploys the chart and runs the suite inside the cluster. |
| `make k8s-down` | Deletes the kind cluster. |

Controller and supervisor unit tests are run with `go test ./...` from the
repository root. See the [test guide](../tests/README.md) for the layout,
environment variables and debugging tips.

## Linting

```bash
make lint   # release pins, Go, shell, Dockerfiles, Compose, Helm charts and Markdown links
```

`make lint` needs `golangci-lint` (v2.15.0, the version CI uses), `shellcheck`,
`helm`, `lychee` and Docker; hadolint runs from a pinned container image. Each
check fails if its tool is missing, and CI calls the same Make targets.

The current validation toolchain uses Helm v4.3.2, ShellCheck v0.11.0 and
lychee v0.24.2. Run `make go-mod-check go-vulncheck` to check both module files
without modifying them and scan application/test calls with pinned
govulncheck v1.9.0. CI runs these checks as well as Trivy.

Go code is checked with the repository's [`.golangci.yml`](../.golangci.yml),
which also enforces `gofmt`. Suppress a finding only with an inline
`//nolint:<linter> // reason` comment that explains why the code is safe.

Markdown link checking uses `lychee` in offline mode, including local anchors.

`make helm-lint` also runs `tests/kubernetes/helm-chart-test.sh`, which
checks the chart's validation rules and rendered output. Add a case there when
you change chart behaviour.

## Continuous integration

[`build-and-test.yml`](../.github/workflows/build-and-test.yml) runs on pull
requests and pushes. It runs:

- Trivy scans of dependency manifests and every built OCI image; HIGH/CRITICAL
  runtime findings block publishing;
- module consistency and reachable Go vulnerability checks for both modules;
- Go linting and formatting, Markdown link checks, shellcheck, hadolint, and Compose and
  Helm validation;
- Go tests and builds;
- image builds and the Docker integration suite;
- the kind-based Kubernetes suite;
- disposable two-controller acceptance using the official ExternalDNS image,
  including native DNS authorization refusals, verified LDAPS, record lifecycle,
  controller failover and pod-local credential/network boundaries.

The bridge acceptance harness does not contact an existing domain or Kubernetes
cluster. It creates its own internal Docker network and kind cluster, uses a
short-lived test CA and disposable accounts, and removes its controllers, cluster,
network and private test material even on failure. It refuses overlapping host
routes, existing task resource names and nonempty work directories. Diagnostic
logs contain only the disposable domain.

With the local core and bridge images already built, run it using installed
`kind`, `kubectl`, `helm`, `openssl` and Docker:

```bash
workdir="$(mktemp -d)"
CGO_ENABLED=0 go test -c ./internal/services/dnsbridge -o "$workdir/backend-test"
python3 tests/dnsbridge/integration.py \
  --core-image gmouzourou/domain-in-a-box:latest \
  --bridge-image ghcr.io/gmouzourou/domain-in-a-box-external-dns-bridge:latest \
  --backend-test-binary "$workdir/backend-test" \
  --work-dir "$workdir/acceptance" \
  --name dib-bridge-development
```

Override image tags and tool paths using the harness flags when necessary. Do
not run the Docker integration Compose file unchanged on a host whose LAN uses
its test subnet; use the isolated bridge acceptance instead.

Images are published to `ghcr.io/gmouzourou/` (and to Docker Hub when its
credentials are configured) only from `main` and `v*` release tags.

## Dependency maintenance

Dependabot checks Go modules, Dockerfiles and GitHub Actions weekly. Minor/patch
Go and Actions updates are grouped; major updates are reviewed separately.
Keep both modules aligned and migrate deprecated APIs rather than suppressing
their diagnostics. Review release notes for major updates before merging.

Pins outside Dependabot's manifests need explicit review: PostgreSQL images in
Compose/Helm, the ExternalDNS tag/digest, kind/node images, Multus/CNI downloads,
hadolint, scanner versions, the promotion utility, and the Samba source
commit/archive checksum. Update every consumer together and verify checksums,
image identities and the integration suites. Native package versions come from
Ubuntu/ISC repositories; rebuild their cached stages when checking for package
updates and inspect the resulting SBOMs.

The current Kubernetes test set is kind v0.33.0 with its digest-pinned
Kubernetes v1.37.0 node image, Multus v4.3.2 and CNI plugins v1.10.0. CI uses
kubectl v1.37.2. Use this compatible node image rather than assuming any newer
Kubernetes image is supported by kind.
The lab replaces the upstream Multus manifest's `snapshot` image references
with the selected stable release tag.

Ubuntu 26.04 remains the shared base: 26.10 no longer packages
`isc-dhcp-client`, which the Linux client's DHCP checks require. Replacing that
client needs a separately tested migration before raising the OS floor.
The Samba metrics source remains pinned to the stable 4.23.13 release; upgrade
it with the distribution's Samba libraries rather than independently selecting
the newest upstream major. The stable Skopeo image currently packages v1.24.1,
so the promotion helper uses that verified image instead of an unavailable
v1.25.1 image tag. Pin its registry image-index digest, not a local image ID.
CI pulls and runs the pinned promotion tool on pull requests as well as pushes,
so an unavailable pin is caught before the publish-only job.

## Releasing

[`VERSION`](../VERSION) is the single project release version, without the
`v` prefix. Chart version, chart `appVersion`, default controller/Stork image
tags, Compose images, the bridge chart/image and the manual's image examples must
agree with it.
Third-party dependency versions are independent and are not changed by the
release updater.

For a stable release (replace `X.Y.Z` with the new version):

1. From the repository root, run:

   ```bash
   make release-version RELEASE_VERSION=X.Y.Z
   make version-check
   ```

   Commit every changed file together. `make release-version` without an
   argument resynchronizes the pins from the existing `VERSION` file. Do not
   edit the copies individually.
2. Run the relevant tests and `make lint`, then merge/push through the usual
   review process and wait for successful CI.
3. Tag the exact validated commit and push the tag:

   ```bash
   git tag vX.Y.Z <validated-commit>
   git push origin vX.Y.Z
   ```

4. Wait for the tag workflow to pass and publish the three versioned images. Then
   create the GitHub release for that tag, with notes describing changes and
   any migration steps.

`make version-check` is also part of `make lint` and CI. On a tag build it
requires `GITHUB_REF_NAME` to be exactly `v` plus `VERSION`. To check a tag
locally, run `go run ./cmd/dib-release-ctl check --tag vX.Y.Z`.
Besides the chart, Compose and documentation fields it owns, the check scans
every Markdown, YAML, HCL, JSON, template, shell and Dockerfile in the
repository for `ghcr.io/gmouzourou/domain-in-a-box*:X.Y.Z` references, so a new
document cannot pin a stale release; `make release-version` rewrites them too.
Publishing depends on the version-checking lint job, so mismatched versions
cannot be published even if the integration suites pass.

## Documentation

User-facing changes need matching updates to this manual and, where the
change affects features or deployment choice, to the
[project home page](../README.md).
