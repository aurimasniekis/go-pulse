"""The ca option adds to the system roots instead of replacing them."""

import shutil
import ssl
import subprocess

import pytest

from pulse.client import tls_context


@pytest.mark.skipif(shutil.which("openssl") is None, reason="needs openssl")
def test_ca_adds_to_system_roots(tmp_path):
    ca = tmp_path / "ca.pem"
    subprocess.run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes", "-days", "1",
                    "-subj", "/CN=pulse test CA", "-keyout", str(tmp_path / "ca.key"), "-out", str(ca)],
                   check=True, capture_output=True)
    system = ssl.create_default_context().cert_store_stats()["x509_ca"]
    assert tls_context().cert_store_stats()["x509_ca"] == system
    assert tls_context(str(ca)).cert_store_stats()["x509_ca"] == system + 1
