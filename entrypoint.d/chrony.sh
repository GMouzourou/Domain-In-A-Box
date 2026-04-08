#!/bin/sh

# Chrony configuration helpers for Domain-In-A-Box.

dib_configure_chrony() {
    echo "Writing /etc/chrony/chrony.conf..."
    tee /etc/chrony/chrony.conf >/dev/null <<EOF
pool pool.ntp.org iburst
driftfile /var/lib/chrony/chrony.drift
makestep 1.0 3
rtcsync
local stratum 10
allow ${SUBNET}
bindaddress ${IP}
bindcmdaddress 127.0.0.1
ntpsigndsocket /var/lib/samba/ntp_signd
EOF
}
