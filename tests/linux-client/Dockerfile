# Linux test client for testing Active Directory integration
# This client will attempt to join the domain and validate AD functionality
# Uses dbus-launch for realm operations (no systemd required)
FROM golang:1.27.4@sha256:5e8afd493a7124a8586e134d9b532731161d4b9becf9dee5a5956b107426f93d AS test-runner-builder

WORKDIR /src
COPY tests/test-runner/go.mod tests/test-runner/go.sum ./
RUN go mod download
COPY tests/test-runner/ ./
RUN CGO_ENABLED=0 go build -o /out/test-runner .

FROM ubuntu:26.04@sha256:d66b366dac61b575bee2aef07e9c26e8070096fbe84a3d4d3eb0da069b5d76db

ENV DEBIAN_FRONTEND=noninteractive
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# Install basic tools and AD-related packages
RUN apt-get update && \
    apt-get install -y \
    bash \
    curl \
    dnsutils \
    ldap-utils \
    passwd \
    sudo \
    vim \
    dbus \
    packagekit \
    realmd \
    sssd \
    sssd-ad \
    sssd-tools \
    libnss-sss \
    libpam-sss \
    oddjob \
    oddjob-mkhomedir \
    adcli \
    samba-client \
    winbind \
    krb5-user \
    krb5-config \
    isc-dhcp-client && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# Set up D-Bus for realm operations
RUN mkdir -p /var/run/dbus && \
    chmod 755 /var/run/dbus

# Set up a basic user for testing
RUN groupadd -g 1001 testuser && \
    useradd -u 1001 -g testuser -s /bin/bash -m testuser && \
    echo "testuser:testpass123" | chpasswd

COPY --from=test-runner-builder /out/test-runner /usr/local/bin/test-runner

# Copy entrypoint script that starts dbus-daemon and DHCP client
WORKDIR /
COPY tests/linux-client/entrypoint.sh /entrypoint.sh
COPY tests/linux-client/ldap-gssapi-test.sh /usr/local/bin/ldap-gssapi-test.sh
RUN chmod +x /entrypoint.sh

# Entry point
ENTRYPOINT ["/entrypoint.sh"]
CMD ["/usr/local/bin/test-runner","run","all"]
