import logging
import os
from pathlib import Path
import shutil
import subprocess

from samba.auth import system_session
from samba.credentials import Credentials, DONT_USE_KERBEROS
from samba.join import join_DC
from samba.param import LoadParm
from samba.provision import provision


def main():
    realm = "BRIDGE.TEST.ARPA"
    hostname = os.environ["DIB_TEST_HOSTNAME"]
    address = os.environ["DIB_TEST_ADDRESS"]
    password = os.environ["DIB_TEST_ADMIN_PASSWORD"]
    primary = os.environ.get("DIB_TEST_PRIMARY")
    if Path("/state/etc/smb.conf").exists():
        pass
    elif primary:
        lp = LoadParm()
        lp.load_default()
        credentials = Credentials()
        credentials.guess(lp)
        credentials.set_username("Administrator")
        credentials.set_password(password)
        credentials.set_domain("BRIDGE")
        credentials.set_realm(realm)
        credentials.set_kerberos_state(DONT_USE_KERBEROS)
        join_DC(
            logger=logging.getLogger("fixture"), server=primary, creds=credentials, lp=lp,
            site="Default-First-Site-Name", netbios_name=hostname.upper(),
            targetdir="/state", domain=realm, dns_backend="SAMBA_INTERNAL")
    else:
        provision(
            logging.getLogger("fixture"), system_session(), targetdir="/state",
            realm=realm, domain="BRIDGE", hostname=hostname, hostip=address,
            serverrole="active directory domain controller", dns_backend="SAMBA_INTERNAL",
            adminpass=password, useeadb=True, skip_sysvolacl=True)
    tls_directory = Path("/state/private/tls")
    tls_directory.mkdir(parents=True, exist_ok=True)
    for filename in ("ca.pem", "cert.pem", "key.pem"):
        shutil.copyfile("/certs/" + filename, tls_directory / filename)
    (tls_directory / "key.pem").chmod(0o600)
    Path("/etc/resolv.conf").write_text(f"nameserver {primary or address}\n")
    shutil.copyfile("/state/etc/smb.conf", "/etc/samba/smb.conf")
    with Path("/etc/samba/smb.conf").open("a") as configuration:
        configuration.write(
            "\n[global]\n"
            "tls ca file = /state/private/tls/ca.pem\n"
            "tls cert file = /state/private/tls/cert.pem\n"
            "tls key file = /state/private/tls/key.pem\n"
            "interfaces = lo eth0\n"
            "bind interfaces only = yes\n"
            "dreplsrv:periodic_interval = 5\n"
            "rpc server dynamic port range = 49152-49252\n")
    configured = LoadParm()
    configured.load("/etc/samba/smb.conf")
    for parameter, expected in (("tls cafile", "/state/private/tls/ca.pem"),
                                ("tls certfile", "/state/private/tls/cert.pem"),
                                ("tls keyfile", "/state/private/tls/key.pem")):
        if configured.get(parameter) != expected:
            raise RuntimeError(f"{parameter} was not applied to the disposable controller")
    print("Disposable controller TLS paths verified", flush=True)
    os.execv("/usr/sbin/samba", ["samba", "-i", "-s", "/etc/samba/smb.conf"])


if __name__ == "__main__":
    main()
