import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest


import oci_archive


class OCIArtifactTests(unittest.TestCase):
    def fixture(self, directory, *, corrupt=False, wrong_subject=False, missing_sbom=False, sbom_bytes=0):
        files = {}

        def blob(value, media_type):
            data = json.dumps(value, separators=(",", ":")).encode()
            digest = hashlib.sha256(data).hexdigest()
            files["blobs/sha256/" + digest] = data
            return {"digest": "sha256:" + digest, "size": len(data), "mediaType": media_type}

        config = blob({"architecture": "amd64", "os": "linux", "rootfs": {"type": "layers", "diff_ids": []}},
                      "application/vnd.oci.image.config.v1+json")
        image = blob({"schemaVersion": 2, "config": config, "layers": []},
                     "application/vnd.oci.image.manifest.v1+json")
        image["platform"] = {"architecture": "amd64", "os": "linux"}
        predicates = ["https://slsa.dev/provenance/v1"]
        if not missing_sbom:
            predicates.append("https://spdx.dev/Document")
        layers = []
        for predicate in predicates:
            layers.append(blob({
                "_type": "https://in-toto.io/Statement/v1",
                "predicateType": predicate,
                "subject": [{"name": "fixture", "digest": {
                    "sha256": "0" * 64 if wrong_subject else image["digest"].removeprefix("sha256:")}}],
                "predicate": {"document": "x" * sbom_bytes} if predicate == "https://spdx.dev/Document" else {},
            }, "application/vnd.in-toto+json"))
        attestation = blob({"schemaVersion": 2, "config": config, "layers": layers},
                           "application/vnd.oci.image.manifest.v1+json")
        attestation["platform"] = {"architecture": "unknown", "os": "unknown"}
        attestation["annotations"] = {
            "vnd.docker.reference.type": "attestation-manifest",
            "vnd.docker.reference.digest": image["digest"],
        }
        root = blob({"schemaVersion": 2, "manifests": [image, attestation]},
                    "application/vnd.oci.image.index.v1+json")
        files["index.json"] = json.dumps({"schemaVersion": 2, "manifests": [root]}).encode()
        files["oci-layout"] = b'{"imageLayoutVersion":"1.0.0"}'
        if corrupt:
            files["blobs/sha256/" + config["digest"].removeprefix("sha256:")] += b" "
        path = Path(directory) / "image.oci.tar"
        with tarfile.open(path, "w") as archive:
            for name, data in files.items():
                header = tarfile.TarInfo(name)
                header.size = len(data)
                archive.addfile(header, io.BytesIO(data))
        return path, root["digest"], config["digest"]

    def test_preserves_index_config_and_attestation_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            path, index_digest, config_digest = self.fixture(directory)
            result = oci_archive.inspect_archive(path)
            self.assertEqual(result["indexDigest"], index_digest)
            self.assertEqual(result["configDigest"], config_digest)
            self.assertEqual(len(result["predicates"]), 2)

    def test_rejects_corruption_unbound_attestation_and_missing_sbom(self):
        for options, message in [
            ({"corrupt": True}, "content digest mismatch"),
            ({"wrong_subject": True}, "subject is not the tested image"),
            ({"missing_sbom": True}, "missing SPDX SBOM"),
        ]:
            with self.subTest(options=options), tempfile.TemporaryDirectory() as directory:
                path, _, _ = self.fixture(directory, **options)
                with self.assertRaisesRegex(ValueError, message):
                    oci_archive.inspect_archive(path)

    def test_accepts_large_sbom_but_keeps_attestation_size_bounded(self):
        for size, accepted in [(17 * 1024 * 1024, True), (65 * 1024 * 1024, False)]:
            with self.subTest(size=size), tempfile.TemporaryDirectory() as directory:
                path, _, _ = self.fixture(directory, sbom_bytes=size)
                if accepted:
                    self.assertIn("https://spdx.dev/Document", oci_archive.inspect_archive(path)["predicates"])
                else:
                    with self.assertRaisesRegex(ValueError, "exceeds 64 MiB"):
                        oci_archive.inspect_archive(path)


if __name__ == "__main__":
    unittest.main()
