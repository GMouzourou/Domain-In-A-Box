"""Run inside a controller to verify Stork's live agent callbacks."""

import http.cookiejar
import json
import os
import socket
import ssl
import time
import urllib.error
import urllib.request
from pathlib import Path


def connect_with_retry(host: str, port: int, timeout: float = 60) -> socket.socket:
    deadline = time.monotonic() + timeout
    while True:
        remaining = deadline - time.monotonic()
        try:
            connection_timeout = min(10, max(remaining, 0.1))
            return socket.create_connection((host, port), timeout=connection_timeout)
        except OSError as error:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError(
                    f"Could not connect to Stork at {host}:{port} within {timeout:g}s"
                ) from error
            time.sleep(min(1, remaining))


def main() -> None:
    host = os.environ["STORK_SERVER_HOST"]
    scheme = os.environ.get("STORK_SERVER_SCHEME", "https")
    port = int(os.environ.get("STORK_SERVER_PORT", "443"))
    expected_count = int(os.environ.get("STORK_TEST_MACHINE_COUNT", "1"))
    if expected_count < 1 or expected_count > 100:
        raise ValueError("STORK_TEST_MACHINE_COUNT must be between 1 and 100")
    if scheme not in ("http", "https"):
        raise ValueError(f"Unsupported Stork scheme: {scheme}")
    if scheme == "http" and os.environ.get("DIB_ALLOW_INSECURE_STORK_HTTP") != "true":
        raise ValueError("HTTP is allowed only in an explicitly insecure test fixture")

    if scheme == "https":
        for label, context, server_name in (
            ("wrong root", ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT), host),
            ("wrong hostname", ssl.create_default_context(), "wrong-host.invalid"),
        ):
            with connect_with_retry(host, port) as connection:
                try:
                    with context.wrap_socket(connection, server_hostname=server_name):
                        pass
                except ssl.SSLCertVerificationError:
                    print(f"Stork HTTPS rejects {label}")
                else:
                    raise AssertionError(f"Stork HTTPS unexpectedly accepted {label}")

    base = f"{scheme}://{host}:{port}/api"
    opener = urllib.request.build_opener(
        urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())
    )
    method = os.environ.get("STORK_TEST_AUTH_METHOD", "ldap")
    if method == "ldap":
        identifier = "Administrator"
        secret = os.environ["DIB_DOMAIN_ADMIN_PASSWORD"]
    elif method == "internal":
        identifier = os.environ["STORK_TEST_USERNAME"]
        secret = os.environ["STORK_TEST_PASSWORD"]
    else:
        raise ValueError(f"Unsupported test authentication method: {method}")
    credentials = json.dumps(
        {
            "authenticationMethodId": method,
            "identifier": identifier,
            "secret": secret,
        }
    ).encode()
    request = urllib.request.Request(
        base + "/sessions",
        data=credentials,
        headers={"Content-Type": "application/json"},
    )
    expect_rejection = os.environ.get("STORK_TEST_EXPECT_LDAP_REJECTION") == "true"
    try:
        with opener.open(request, timeout=30):
            pass
    except urllib.error.HTTPError as error:
        if expect_rejection and method == "ldap" and error.code == 400:
            print("Stork rejected AD login with the intentionally invalid LDAP TLS configuration")
            return
        raise
    try:
        if expect_rejection:
            raise AssertionError("Stork unexpectedly accepted invalid LDAP TLS configuration")
        expected = Path("/var/lib/stork-agent/.stork-agent-registered").read_text().strip()
        if not expected:
            raise AssertionError("Stork registration marker is empty")
        with opener.open(base + "/machines?limit=100", timeout=30) as response:
            machines = json.load(response)
        if machines["total"] != expected_count or len(machines["items"]) != expected_count:
            raise AssertionError(f"Expected exactly {expected_count} registered machines")
        matching = [machine for machine in machines["items"] if machine["address"] == expected]
        if len(matching) != 1 or not matching[0]["authorized"]:
            raise AssertionError("Stork machine does not match the authorized callback endpoint")
        machine = matching[0]

        # This endpoint contacts the agent, rather than returning stored registration alone.
        with opener.open(base + f"/machines/{machine['id']}/state", timeout=30) as response:
            state = json.load(response)
        daemons = {daemon["name"]: daemon for daemon in state["daemons"]}
        for name in ("named", "dhcp4"):
            daemon = daemons.get(name)
            if (
                daemon is None
                or not daemon["active"]
                or not daemon["monitored"]
                or not daemon["version"]
                or daemon["uptime"] <= 0
            ):
                raise AssertionError(f"Stork did not observe a live, monitored {name} daemon")
        print(f"Stork authenticated using {method} and observed live BIND/Kea via {expected}")
    finally:
        opener.open(
            urllib.request.Request(base + "/sessions", method="DELETE"), timeout=10
        ).close()


if __name__ == "__main__":
    main()
