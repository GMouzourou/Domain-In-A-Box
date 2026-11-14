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

## Documentation

User-facing changes need matching updates to this manual and, where the
change affects features or deployment choice, to the
[project home page](../README.md).
