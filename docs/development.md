# Development

Contributions are welcome. Open an issue to discuss significant changes
before starting work.

## Repository layout

| Path | Contents |
| --- | --- |
| `Dockerfile` | Multi-stage build for the controller (`core`) and Stork (`stork`) images. |
| `entrypoint.sh` | Controller start-up: interface discovery, provisioning or joining, and identity checks. |
| `cmd/`, `internal/` | Go service supervisors and control tools such as `dib-network-core-ctl`. |
| `charts/domain-in-a-box/` | Helm chart. |
| `docker-compose.yml`, `.env.example` | Docker Compose deployment. |
| `tests/` | Integration test runner, Linux test client and Kubernetes test environment. |
| `docs/` | This manual. |

## Building

```bash
make build         # controller image
make build-stork   # Stork image
make help          # all targets
```

Local Make builds default to the development tag `latest`; override it with
`VERSION=<tag>` when needed. This Make variable is not the release-version
source. Production Compose/Helm defaults use pinned release tags, and the test
stack uses its separate `:ci` tags.

## Testing

| Command | What it does |
| --- | --- |
| `make test-unit` | Go unit tests for the test runner. |
| `make test-all` | Builds the images and runs the Docker integration suite: health, DNS, DHCP, AD, and a Linux client domain join. |
| `make test-dns`, `make test-dhcp`, `make test-ad`, `make test-ad-linux` | Individual suites against a running test stack. |
| `make k8s-all` | Creates a kind cluster with Multus and macvlan, deploys the chart and runs the suite inside the cluster. |
| `make k8s-down` | Deletes the kind cluster. |

Controller and supervisor unit tests are run with `go test ./...` from the
repository root. See the [test guide](../tests/README.md) for the layout,
environment variables and debugging tips.

## Linting

```bash
make lint   # shellcheck, hadolint, Compose validation and Helm chart checks
```

`make helm-lint` also runs `tests/kubernetes/helm-chart-test.sh`, which
checks the chart's validation rules and rendered output. Add a case there when
you change chart behaviour.

## Continuous integration

[`build-and-test.yml`](../.github/workflows/build-and-test.yml) runs on pull
requests and pushes. It runs:

- a Trivy security scan;
- Go formatting, shellcheck, hadolint, and Compose and Helm validation;
- Go tests and builds;
- image builds and the Docker integration suite;
- the kind-based Kubernetes suite.

Images are published to `ghcr.io/gmouzourou/` (and to Docker Hub when its
credentials are configured) only from `main` and `v*` release tags.

## Releasing

[`VERSION`](../VERSION) is the single project release version, without the
`v` prefix. Chart version, chart `appVersion`, default controller/Stork image
tags, Compose images and the Docker CLI example must agree with it.
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

4. Wait for the tag workflow to pass and publish both versioned images. Then
   create the GitHub release for that tag, with notes describing changes and
   any migration steps.

`make version-check` is also part of `make lint` and CI. On a tag build it
requires `GITHUB_REF_NAME` to be exactly `v` plus `VERSION`. To check a tag
locally, run `go run ./cmd/dib-release-ctl check --tag vX.Y.Z`.
Publishing depends on the version-checking lint job, so mismatched versions
cannot be published even if the integration suites pass.

## Documentation

User-facing changes need matching updates to this manual and, where the
change affects features or deployment choice, to the
[project home page](../README.md).
