#!/bin/sh

# PostgreSQL helpers for Domain-In-A-Box.

dib_pg_major_version() {
    version=$(find /usr/lib/postgresql -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort -V | tail -n1)
    if [ -z "${version}" ]; then
        echo "Unable to determine PostgreSQL major version from /usr/lib/postgresql" >&2
        return 1
    fi
    printf '%s' "${version}"
}

dib_configure_postgresql() {
    echo "Configuring PostgreSQL runtime directories..."
    mkdir -p /run/postgresql
    chown postgres:postgres /run/postgresql
    chmod 775 /run/postgresql

    version=$(dib_pg_major_version)

    if pg_lsclusters -h | awk '$1 == "'"${version}"'" && $2 == "main" { found = 1 } END { exit(found ? 0 : 1) }'; then
        echo "Keeping existing PostgreSQL cluster ${version}/main"
        return 0
    fi

    echo "Creating PostgreSQL cluster ${version}/main..."
    pg_createcluster "${version}" main --start-conf=manual >/dev/null
}

dib_validate_postgresql_cluster() {
    version=$(dib_pg_major_version)

    if ! pg_lsclusters -h | awk '$1 == "'"${version}"'" && $2 == "main" { found = 1 } END { exit(found ? 0 : 1) }'; then
        echo "PostgreSQL cluster ${version}/main is missing" >&2
        return 1
    fi
}

if [ "${1:-}" = "run" ]; then
    set -eu

    version=$(dib_pg_major_version)

    exec /usr/lib/postgresql/"${version}"/bin/postgres \
        -D /var/lib/postgresql/"${version}"/main \
        -c config_file=/etc/postgresql/"${version}"/main/postgresql.conf
fi