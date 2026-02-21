# Linux test client for testing Active Directory integration
# This client will attempt to join the domain and validate AD functionality
# Uses dbus-launch for realm operations (no systemd required)

FROM ubuntu:22.04

# Prevent interactive prompts during installation
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
    netcat \
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
    isc-dhcp-client \
    golang-go \
    git && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# Set up D-Bus for realm operations
RUN mkdir -p /var/run/dbus && \
    chmod 755 /var/run/dbus

# Set up a basic user for testing
RUN groupadd -g 1000 testuser && \
    useradd -u 1000 -g testuser -s /bin/bash -m testuser && \
    echo "testuser:testpass123" | chpasswd

# Copy and build Go CLI test runner (replaces shell scripts)
COPY tests/test-runner /tests/test-runner
WORKDIR /tests/test-runner
RUN go mod tidy && go build -o /usr/local/bin/test-runner && chmod +x /usr/local/bin/test-runner

# Copy entrypoint script that starts dbus-daemon and DHCP client
WORKDIR /
COPY tests/linux-client/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Entry point
ENTRYPOINT ["/entrypoint.sh"]
CMD ["/usr/local/bin/test-runner","run","all"]
