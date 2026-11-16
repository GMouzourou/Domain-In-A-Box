import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile


SKOPEO_IMAGE = "quay.io/skopeo/stable:v1.24.1@sha256:1b069d441808768b6fa4fbafd288e970ce052598c9c6e396148c0076f7bb0cf9"


def inspect_archive(path):
    with tarfile.open(path, "r") as archive:
        members = {}
        for member in archive.getmembers():
            if member.isdir():
                continue
            if not member.isfile() or member.name in members:
                raise ValueError("OCI archive has links or duplicate entries")
            if member.name not in ("index.json", "oci-layout") and not member.name.startswith("blobs/sha256/"):
                raise ValueError(f"unexpected OCI archive entry: {member.name}")
            members[member.name] = member

        def read_json(name, limit=16 * 1024 * 1024):
            member = members[name]
            if member.size > limit:
                raise ValueError(f"OCI JSON metadata exceeds {limit // (1024 * 1024)} MiB")
            return json.load(archive.extractfile(member))

        for name, member in members.items():
            if not name.startswith("blobs/sha256/"):
                continue
            expected = name.removeprefix("blobs/sha256/")
            if len(expected) != 64 or any(character not in "0123456789abcdef" for character in expected):
                raise ValueError("invalid content-addressed blob path")
            with archive.extractfile(member) as stream:
                actual = hashlib.file_digest(stream, "sha256").hexdigest()
            if actual != expected:
                raise ValueError(f"OCI blob content digest mismatch: {name}")

        def descriptor_json(descriptor, limit=16 * 1024 * 1024):
            digest = descriptor["digest"]
            if not digest.startswith("sha256:"):
                raise ValueError("only SHA-256 OCI descriptors are supported")
            name = "blobs/sha256/" + digest.removeprefix("sha256:")
            if members[name].size != descriptor["size"]:
                raise ValueError("OCI descriptor size mismatch")
            return read_json(name, limit)

        layout = read_json("index.json")
        if read_json("oci-layout") != {"imageLayoutVersion": "1.0.0"} or len(layout["manifests"]) != 1:
            raise ValueError("expected one OCI image index in the archive")
        root = layout["manifests"][0]
        index = descriptor_json(root)
        if root["mediaType"] != "application/vnd.oci.image.index.v1+json" or index["schemaVersion"] != 2:
            raise ValueError("expected an attested OCI image index")
        images = [item for item in index["manifests"] if item.get("platform") == {"architecture": "amd64", "os": "linux"}]
        attestations = [item for item in index["manifests"]
                        if item.get("annotations", {}).get("vnd.docker.reference.type") == "attestation-manifest"]
        if len(images) != 1 or len(attestations) != 1 or len(index["manifests"]) != 2:
            raise ValueError("expected exactly one linux/amd64 image and its attestation manifest")
        image = images[0]
        manifest = descriptor_json(image)
        config = descriptor_json(manifest["config"])
        if config["architecture"] != "amd64" or config["os"] != "linux":
            raise ValueError("image configuration does not match the tested platform")
        for layer in manifest["layers"]:
            name = "blobs/sha256/" + layer["digest"].removeprefix("sha256:")
            if not layer["digest"].startswith("sha256:") or members[name].size != layer["size"]:
                raise ValueError("image layer descriptor mismatch")
        attestation = attestations[0]
        if attestation["annotations"]["vnd.docker.reference.digest"] != image["digest"]:
            raise ValueError("attestation references a different image")
        predicates = set()
        for layer in descriptor_json(attestation)["layers"]:
            statement = descriptor_json(layer, limit=64 * 1024 * 1024)
            if not any(subject["digest"].get("sha256") == image["digest"].removeprefix("sha256:")
                       for subject in statement["subject"]):
                raise ValueError("attestation subject is not the tested image")
            predicates.add(statement["predicateType"])
        if "https://spdx.dev/Document" not in predicates or not any(
                predicate.startswith("https://slsa.dev/provenance/") for predicate in predicates):
            raise ValueError("OCI artifact is missing SPDX SBOM or SLSA provenance")
        return {"indexDigest": root["digest"], "manifestDigest": image["digest"],
                "configDigest": manifest["config"]["digest"], "rootfsDiffIDs": config["rootfs"]["diff_ids"],
                "predicates": sorted(predicates)}


def load_image(path, tag, identity):
    subprocess.run(["docker", "load", "-i", str(path)], check=True)
    actual = json.loads(subprocess.check_output(
        ["docker", "image", "inspect", identity["indexDigest"]]))[0]
    if actual["Descriptor"]["digest"] != identity["indexDigest"]:
        raise ValueError("Docker did not retain the original OCI image index; enable the containerd image store")
    if actual["RootFS"]["Layers"] != identity["rootfsDiffIDs"]:
        raise ValueError("Docker's selected image filesystem is not the tested OCI filesystem")
    subprocess.run(["docker", "tag", identity["indexDigest"], tag], check=True)


def promote_image(path, tags, identity):
    auth = Path.home() / ".docker" / "config.json"
    if not auth.is_file() or not tags:
        raise ValueError("registry credentials and at least one destination tag are required")
    command = ["docker", "run", "--rm",
               "--mount", f"type=bind,src={path.parent},dst=/images,readonly",
               "--mount", f"type=bind,src={auth},dst=/auth/config.json,readonly", SKOPEO_IMAGE]
    for tag in tags:
        subprocess.run(command + ["copy", "--authfile", "/auth/config.json", "--all", "--preserve-digests",
                                 f"oci-archive:/images/{path.name}", "docker://" + tag], check=True)
        raw = subprocess.check_output(command + ["inspect", "--authfile", "/auth/config.json", "--raw", "docker://" + tag])
        if "sha256:" + hashlib.sha256(raw).hexdigest() != identity["indexDigest"]:
            raise ValueError(f"registry changed the tested OCI index: {tag}")


def main():
    parser = argparse.ArgumentParser(description="Verify, natively load, or digest-preserving promote tested OCI images")
    parser.add_argument("operation", choices=["verify", "load", "promote"])
    parser.add_argument("archive", type=Path)
    parser.add_argument("--tag")
    parser.add_argument("--tags-env", help="Environment variable containing newline-separated destination tags")
    args = parser.parse_args()
    path = args.archive.resolve(strict=True)
    identity = inspect_archive(path)
    if args.operation == "load":
        if not args.tag:
            parser.error("load requires --tag")
        load_image(path, args.tag, identity)
    elif args.operation == "promote":
        if not args.tags_env:
            parser.error("promote requires --tags-env")
        promote_image(path, [tag for tag in os.environ[args.tags_env].splitlines() if tag], identity)
    print(json.dumps(identity))


if __name__ == "__main__":
    main()
