import argparse
import ipaddress
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import urllib.error
import urllib.request


REALM = "BRIDGE.TEST.ARPA"
ZONE = "bridge.test.arpa"
MEDIA_TYPE = "application/external.dns.webhook+json;version=1"
REPO = Path(__file__).resolve().parents[2]


def run(command, *, input=None, env=None, timeout=360):
    result = subprocess.run(command, input=input, env=env, text=True, capture_output=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f"command failed: {command}\n{result.stdout}\n{result.stderr}")
    return result.stdout.strip()


def wait(command, expected, timeout=180):
    deadline = time.monotonic() + timeout
    last = ""
    while time.monotonic() < deadline:
        result = subprocess.run(command, text=True, capture_output=True, timeout=30)
        last = result.stdout.strip() if result.returncode == 0 else result.stderr.strip()
        if result.returncode == 0 and expected(last):
            return last
        time.sleep(2)
    raise RuntimeError(f"timed out waiting for {command}: {last}")


def certificate(directory, name, hostname=None):
    key = directory / f"{name}.key"
    cert = directory / f"{name}.pem"
    if hostname is None:
        run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-sha256", "-days", "2",
             "-keyout", str(key), "-out", str(cert), "-subj", f"/CN={name}",
             "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign,cRLSign"])
    else:
        csr = directory / f"{name}.csr"
        extensions = directory / f"{name}.ext"
        extensions.write_text(f"subjectAltName=DNS:{hostname}\nextendedKeyUsage=serverAuth\n"
                              "basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\n")
        run(["openssl", "req", "-new", "-newkey", "rsa:2048", "-nodes", "-keyout", str(key),
             "-out", str(csr), "-subj", f"/CN={hostname}"])
        run(["openssl", "x509", "-req", "-in", str(csr), "-CA", str(directory / "ca.pem"),
             "-CAkey", str(directory / "ca.key"), "-CAcreateserial", "-out", str(cert),
             "-days", "2", "-sha256", "-extfile", str(extensions)])
    key.chmod(0o600)
    return cert, key


def main():
    parser = argparse.ArgumentParser(description="Disposable official ExternalDNS and two-controller acceptance")
    parser.add_argument("--core-image", required=True)
    parser.add_argument("--bridge-image", required=True)
    parser.add_argument("--backend-test-binary", required=True, type=Path)
    parser.add_argument("--kind", default="kind")
    parser.add_argument("--kubectl", default="kubectl")
    parser.add_argument("--helm", default="helm")
    parser.add_argument("--work-dir", required=True, type=Path)
    parser.add_argument("--name", required=True)
    args = parser.parse_args()
    if not args.name.startswith("dib-bridge-") or any(character not in "abcdefghijklmnopqrstuvwxyz0123456789-" for character in args.name):
        parser.error("name must be a task-owned lowercase dib-bridge- prefix")
    directory = args.work_dir.resolve()
    if directory.exists() and any(directory.iterdir()):
        raise RuntimeError("integration work directory must be new or empty")
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    directory.chmod(0o700)
    network = args.name + "-network"
    subnet = ipaddress.ip_network("198.18.80.0/24")
    for route in json.loads(run(["ip", "-j", "-4", "route", "show"])):
        if route["dst"] != "default" and subnet.overlaps(ipaddress.ip_network(route["dst"], strict=False)):
            raise RuntimeError("disposable subnet overlaps an existing host route")
    containers = []
    network_created = False
    cluster_created = False
    forward = None
    kubeconfig = directory / "kubeconfig"
    namespace = "bridge-test"
    kubectl = [args.kubectl, "--kubeconfig", str(kubeconfig)]
    command_env = {**os.environ, "DIB_TEST_ADMIN_PASSWORD": "Disposable-Controller123!"}
    existing = set(run(["docker", "ps", "-a", "--format", "{{.Names}}"]).splitlines())
    if any(args.name + "-" + hostname in existing for hostname in ("dc1", "dc2", "control-plane")):
        raise RuntimeError("task resource names are already in use")
    if args.name in run([args.kind, "get", "clusters"]).splitlines():
        raise RuntimeError("task cluster name is already in use")
    try:
        certificate(directory, "ca")
        certificate(directory, "wrong-ca")
        for name in ("dc1", "dc2"):
            cert, key = certificate(directory, name, f"{name}.{ZONE}")
            certdir = directory / name
            certdir.mkdir()
            (certdir / "cert.pem").write_bytes(cert.read_bytes())
            (certdir / "key.pem").write_bytes(key.read_bytes())
            (certdir / "key.pem").chmod(0o600)
            (certdir / "ca.pem").write_bytes((directory / "ca.pem").read_bytes())
        password = directory / "password"
        password.write_text("Disposable-Bridge123!")
        password.chmod(0o600)
        admin_password = directory / "admin-password"
        admin_password.write_text(command_env["DIB_TEST_ADMIN_PASSWORD"])
        admin_password.chmod(0o600)
        config = {
            "realm": REALM, "zone": ZONE, "username": "dib-edns", "ownerID": "ci-bridge",
            "passwordFile": "/fixture/password", "caFile": "/fixture/ca.pem",
            "controllers": [{"hostname": f"dc1.{ZONE}", "address": "198.18.80.8"},
                            {"hostname": f"dc2.{ZONE}", "address": "198.18.80.9"}],
            "allowedNetworks": ["198.18.80.0/24", "fd12:8036::/64"],
            "protectedNames": ["reserved"],
        }
        (directory / "config.json").write_text(json.dumps(config))
        run(["docker", "network", "create", "--internal", "--subnet", str(subnet),
             "--gateway", "198.18.80.254", network])
        network_created = True
        for index, hostname in enumerate(("dc1", "dc2")):
            name = args.name + "-" + hostname
            address = f"198.18.80.{8 + index}"
            command = ["docker", "run", "-d", "--name", name, "--hostname", hostname, "--network", network,
                       "--ip", address, "--memory", "1g", "--cpus", "1",
                       "--add-host", f"dc1.{ZONE}:198.18.80.8", "--add-host", f"dc2.{ZONE}:198.18.80.9",
                       "--env", "DIB_TEST_ADMIN_PASSWORD", "--env", f"DIB_TEST_HOSTNAME={hostname}",
                       "--env", f"DIB_TEST_ADDRESS={address}",
                       "--mount", f"type=bind,src={REPO / 'tests/dnsbridge/controller-fixture.py'},dst=/controller-fixture.py,readonly",
                       "--mount", f"type=bind,src={directory / hostname},dst=/certs,readonly",
                       "--mount", f"type=bind,src={directory},dst=/fixture,readonly"]
            if index:
                command += ["--dns", "198.18.80.8", "--env", "DIB_TEST_PRIMARY=198.18.80.8"]
            command += ["--entrypoint", "python3", args.core_image, "/controller-fixture.py"]
            containers.append(name)
            run(command, env=command_env)
            wait(["docker", "exec", name, "python3", "-c",
                  "import socket; socket.create_connection(('127.0.0.1',636),2).close()"], lambda _: True)
        primary, secondary = containers
        seed = (
            "from samba.param import LoadParm; from samba.samdb import SamDB; from samba.auth import system_session; "
            "from samba.dcerpc import dnsp; from samba.ndr import ndr_pack; "
            "lp=LoadParm(); lp.load('/etc/samba/smb.conf'); "
            "db=SamDB(url=lp.private_path('sam.ldb'),session_info=system_session(),lp=lp); "
            "r=dnsp.DnssrvRpcRecord(); r.version=5; r.rank=240; r.wType=1; r.dwTtlSeconds=60; r.data='198.18.80.77'; "
            "db.add({'dn':'DC=manual,DC=bridge.test.arpa,CN=MicrosoftDNS,DC=DomainDnsZones,'+str(db.domain_dn()),"
            "'objectClass':'dnsNode','dnsRecord':ndr_pack(r)})"
        )
        run(["docker", "exec", primary, "python3", "-c", seed])
        run(["docker", "exec", primary, "dib-external-dns-bridge", "provision",
             "--bridge-config", "/fixture/config.json"])
        wait(["docker", "exec", secondary, "samba-tool", "user", "show", "dib-edns"], lambda _: True)
        run(["docker", "cp", str(args.backend_test_binary), primary + ":/backend-test"])
        print(run(["docker", "exec", "--env", "DIB_DNS_BRIDGE_INTEGRATION_CONFIG=/fixture/config.json",
             "--env", "DIB_DNS_BRIDGE_INTEGRATION_BAD_CA=/fixture/wrong-ca.pem",
             "--env", "DIB_DNS_BRIDGE_INTEGRATION_ADMIN_PASSWORD=/fixture/admin-password",
             primary, "/backend-test", "-test.run", "^TestADBackendIntegration$", "-test.v"]), flush=True)

        kind_config = directory / "kind.yaml"
        kind_config.write_text("kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\n"
                               "networking:\n  apiServerAddress: 127.0.0.1\n"
                               "nodes:\n  - role: control-plane\n")
        cluster_created = True
        run([args.kind, "create", "cluster", "--name", args.name, "--kubeconfig", str(kubeconfig),
             "--config", str(kind_config), "--wait", "120s"])
        run(["docker", "network", "connect", "--ip", "198.18.80.20", network, args.name + "-control-plane"])
        run([args.kind, "load", "docker-image", "--name", args.name, args.core_image, args.bridge_image])
        run(kubectl + ["create", "namespace", namespace])
        run(kubectl + ["-n", namespace, "create", "secret", "generic", "ci-dns-credentials",
                       "--from-file=password=" + str(password)])
        run(kubectl + ["-n", namespace, "create", "secret", "generic", "ci-dns-trust",
                       "--from-file=ca.crt=" + str(directory / "ca.pem")])
        repository, tag = args.bridge_image.rsplit(":", 1)
        run([args.helm, "--kubeconfig", str(kubeconfig), "upgrade", "--install", "dns",
             str(REPO / "charts/domain-in-a-box-external-dns-bridge"), "-n", namespace,
             "-f", str(REPO / "tests/dnsbridge/values.ci.yaml"),
             "--set", f"bridgeImage.repository={repository}", "--set", f"bridgeImage.tag={tag}",
             "--set", "bridgeImage.pullPolicy=Never", "--set", "protectedNames[0]=reserved",
             "--set", f"watchNamespace={namespace}", "--wait", "--timeout", "180s"])
        pod = json.loads(run(kubectl + ["-n", namespace, "get", "pods", "-l", "app.kubernetes.io/instance=dns", "-o", "json"]))["items"][0]
        name = pod["metadata"]["name"]
        mounts = {container["name"]: {mount["name"] for mount in container["volumeMounts"]} for container in pod["spec"]["containers"]}
        if mounts["external-dns"] != {"kubernetes-token"} or mounts["dns-bridge"] != {"config", "credentials", "trust", "tickets"}:
            raise AssertionError("Kubernetes and DNS credentials were not isolated")
        if pod["spec"].get("shareProcessNamespace") or pod["spec"].get("hostNetwork"):
            raise AssertionError("chart exposed process or host network namespaces")
        account = pod["spec"]["serviceAccountName"]
        authorization = subprocess.run(
            kubectl + ["auth", "can-i", "get", "secrets", "-n", namespace,
                       "--as", f"system:serviceaccount:{namespace}:{account}"],
            text=True, capture_output=True)
        if authorization.returncode != 1 or authorization.stdout.strip() != "no":
            raise AssertionError("ExternalDNS can read Kubernetes Secrets")
        run(kubectl + ["-n", namespace, "run", "dns-network-probe", "--image", args.core_image,
                       "--image-pull-policy=Never", "--restart=Never",
                       "--overrides", json.dumps({"spec": {"serviceAccountName": account,
                                                          "automountServiceAccountToken": True}}),
                       "--command", "--", "sleep", "infinity"])
        run(kubectl + ["-n", namespace, "wait", "--for=condition=Ready", "pod/dns-network-probe", "--timeout=120s"])
        run(kubectl + ["-n", namespace, "exec", "dns-network-probe", "--", "python3", "-c",
                       "import json, os, ssl, urllib.request; from pathlib import Path; "
                       "p=Path('/var/run/secrets/kubernetes.io/serviceaccount'); "
                       "context=ssl.create_default_context(cafile=str(p/'ca.crt')); "
                       "url='https://'+os.environ['KUBERNETES_SERVICE_HOST']+':'+os.environ['KUBERNETES_SERVICE_PORT']"
                       f"+'/apis/networking.k8s.io/v1/namespaces/{namespace}/ingresses'; "
                       "request=urllib.request.Request(url,headers={'Authorization':'Bearer '+(p/'token').read_text()}); "
                       "response=urllib.request.urlopen(request,context=context,timeout=10); "
                       "assert json.load(response)['kind']=='IngressList'"])
        run(kubectl + ["-n", namespace, "exec", "dns-network-probe", "--", "python3", "-c",
                       "import socket; s=socket.socket(); s.settimeout(3); "
                       f"code=s.connect_ex(({pod['status']['podIP']!r},8888)); "
                       "s.close(); assert code != 0, 'webhook reachable outside its pod'"])
        forward = subprocess.Popen(
            kubectl + ["-n", namespace, "port-forward", "pod/" + name, "0:8888", "--address=127.0.0.1"],
            text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        forwarding = forward.stdout.readline().strip()
        if not forwarding.startswith("Forwarding from 127.0.0.1:"):
            raise RuntimeError("private test port-forward did not become ready: " + forwarding)
        port = forwarding.split("127.0.0.1:")[1].split()[0]
        url = "http://127.0.0.1:" + port
        with urllib.request.urlopen(urllib.request.Request(url, headers={"Accept": MEDIA_TYPE}), timeout=5) as response:
            if json.load(response)["filters"] != [ZONE]:
                raise AssertionError("test port-forward did not reach the expected bridge")
        for forbidden in ("wpad." + ZONE, "outside.test.arpa"):
            addresses = {"dnsName": forbidden, "recordType": "A", "recordTTL": 60, "targets": ["198.18.80.50"]}
            request = urllib.request.Request(
                url + "/records", data=json.dumps({"create": [addresses]}).encode(),
                headers={"Content-Type": MEDIA_TYPE}, method="POST")
            try:
                urllib.request.urlopen(request, timeout=35)
            except urllib.error.HTTPError as error:
                if error.code != 400:
                    raise
            else:
                raise AssertionError("direct webhook mutation escaped flat-name policy")

        def ingress(resource, address, hostname=None, ipv6=None):
            value = {"apiVersion": "networking.k8s.io/v1", "kind": "Ingress",
                     "metadata": {"name": resource, "namespace": namespace},
                     "spec": {"rules": [{"http": {"paths": [{"path": "/", "pathType": "Prefix",
                              "backend": {"service": {"name": "unused", "port": {"number": 80}}}}]}}]}}
            if hostname:
                value["spec"]["rules"][0]["host"] = hostname
            run(kubectl + ["apply", "-f", "-"], input=json.dumps(value))
            targets = [{"ip": address}]
            if ipv6:
                targets.append({"ip": ipv6})
            run(kubectl + ["-n", namespace, "patch", "ingress", resource, "--subresource=status", "--type=merge",
                           "-p", json.dumps({"status": {"loadBalancer": {"ingress": targets}}})])

        def answer(controller, resource, kind, expected):
            address = "198.18.80.8" if controller == primary else "198.18.80.9"
            return wait(["docker", "exec", controller, "dig", "@" + address, resource + "." + ZONE, kind, "+short"],
                        lambda value: value == expected if kind in {"A", "AAAA"} else expected in value if expected else value == "")

        ingress("reserved", "198.18.80.50", "reserved." + ZONE)
        ingress("manual", "198.18.80.50", "manual." + ZONE)
        ingress("app", "198.18.80.50", ipv6="fd12:8036::50")
        for controller in containers:
            answer(controller, "app", "A", "198.18.80.50")
            answer(controller, "app", "AAAA", "fd12:8036::50")
            answer(controller, "dib-edns-a-app", "TXT", "external-dns/owner=ci-bridge")
            answer(controller, "dib-edns-aaaa-app", "TXT", "external-dns/owner=ci-bridge")
            answer(controller, "manual", "A", "198.18.80.77")
            answer(controller, "reserved", "A", "")
            answer(controller, "dib-edns-a-reserved", "TXT", "")
        ingress("app", "198.18.80.51", ipv6="fd12:8036::51")
        for controller in containers:
            answer(controller, "app", "A", "198.18.80.51")
            answer(controller, "app", "AAAA", "fd12:8036::51")
        run(kubectl + ["-n", namespace, "delete", "ingress", "app"])
        for controller in containers:
            answer(controller, "app", "A", "")
            answer(controller, "app", "AAAA", "")
            answer(controller, "dib-edns-a-app", "TXT", "")
            answer(controller, "dib-edns-aaaa-app", "TXT", "")
        run(["docker", "stop", primary])
        ingress("failover", "198.18.80.52")
        answer(secondary, "failover", "A", "198.18.80.52")
        run(["docker", "start", primary])
        answer(primary, "failover", "A", "198.18.80.52")
        run(kubectl + ["-n", namespace, "delete", "ingress", "failover"])
        for controller in containers:
            answer(controller, "failover", "A", "")
        print("PASS: official ExternalDNS flat-name create/update/delete, ownership, protected/manual preservation and controller failover")
    finally:
        original_error = sys.exc_info()[1]
        cleanup_errors = []

        def cleanup(command):
            result = subprocess.run(command, text=True, capture_output=True)
            if result.returncode:
                cleanup_errors.append(f"{command}: {result.stdout}\n{result.stderr}")

        if forward is not None:
            if forward.poll() is None:
                forward.terminate()
            forward.wait(timeout=10)
        for name in containers:
            result = subprocess.run(["docker", "logs", name], text=True, capture_output=True)
            (directory / (name + ".log")).write_text(result.stdout + result.stderr)
            result = subprocess.run(
                ["docker", "exec", name, "samba-tool", "drs", "showrepl", "--summary"],
                text=True, capture_output=True, timeout=30)
            (directory / (name + "-replication.log")).write_text(result.stdout + result.stderr)
            result = subprocess.run(
                ["docker", "exec", name, "openssl", "s_client", "-connect", "127.0.0.1:636",
                 "-CAfile", "/certs/ca.pem", "-showcerts"],
                input="", text=True, capture_output=True, timeout=10)
            (directory / (name + "-tls.log")).write_text(result.stdout + result.stderr)
        if cluster_created:
            result = subprocess.run(kubectl + ["-n", namespace, "logs", "-l", "app.kubernetes.io/instance=dns",
                                              "--all-containers", "--prefix", "--tail=-1"], text=True, capture_output=True)
            (directory / "external-dns.log").write_text(result.stdout + result.stderr)
            result = subprocess.run(kubectl + ["-n", namespace, "logs", "-l", "app.kubernetes.io/instance=dns",
                                              "-c", "external-dns", "--previous", "--tail=-1"], text=True, capture_output=True)
            (directory / "external-dns-previous.log").write_text(result.stdout + result.stderr)
            result = subprocess.run(kubectl + ["-n", namespace, "describe", "pods",
                                              "-l", "app.kubernetes.io/instance=dns"], text=True, capture_output=True)
            (directory / "external-dns-pods.log").write_text(result.stdout + result.stderr)
            cleanup([args.kind, "delete", "cluster", "--name", args.name])
        for name in containers:
            cleanup(["docker", "rm", "-f", name])
        if network_created:
            cleanup(["docker", "network", "rm", network])
        for filename in ("ca.key", "wrong-ca.key", "dc1.key", "dc2.key"):
            path = directory / filename
            if path.exists():
                path.unlink()
        password = directory / "password"
        if password.exists():
            password.unlink()
        admin_password = directory / "admin-password"
        if admin_password.exists():
            admin_password.unlink()
        if kubeconfig.exists():
            kubeconfig.unlink()
        for hostname in ("dc1", "dc2"):
            key = directory / hostname / "key.pem"
            if key.exists():
                key.unlink()
        if cleanup_errors:
            message = "integration cleanup failed:\n" + "\n".join(cleanup_errors)
            if original_error is None:
                raise RuntimeError(message)
            print(message, file=sys.stderr)


if __name__ == "__main__":
    main()
