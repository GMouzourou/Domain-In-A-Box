import importlib.util
import socket
import threading
import unittest
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest import mock

spec = importlib.util.spec_from_file_location(
    "stork_management", Path(__file__).with_name("stork-management-test.py")
)
assert spec is not None and spec.loader is not None
stork_management = importlib.util.module_from_spec(spec)
spec.loader.exec_module(stork_management)


class RequestHandler(BaseHTTPRequestHandler):
    requests = 0
    status = 200

    def do_POST(self):
        type(self).requests += 1
        self.rfile.read(int(self.headers["Content-Length"]))
        self.send_response(self.status)
        self.end_headers()

    def log_message(self, *_):
        pass


class ConnectionRetryTests(unittest.TestCase):
    def setUp(self):
        RequestHandler.requests = 0
        RequestHandler.status = 200
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), RequestHandler)
        self.thread = threading.Thread(target=self.server.serve_forever)
        self.thread.start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.thread.join)
        self.addCleanup(self.server.shutdown)
        self.url = f"http://127.0.0.1:{self.server.server_port}/sessions"
        self.opener = urllib.request.build_opener(stork_management.RetryingHTTPHandler())

    def test_connection_timeout_is_retried_before_post(self):
        original_connect = socket.create_connection
        attempts = 0

        def connect(address, *args, **kwargs):
            nonlocal attempts
            attempts += 1
            if attempts == 1:
                raise TimeoutError("stale endpoint")
            return original_connect(address, *args, **kwargs)

        with (
            mock.patch.object(socket, "create_connection", side_effect=connect),
            mock.patch.object(stork_management.time, "sleep"),
            self.opener.open(self.url, data=b"credentials", timeout=5) as response,
        ):
            self.assertEqual(response.status, 200)
        self.assertEqual(attempts, 2)
        self.assertEqual(RequestHandler.requests, 1)

    def test_http_failure_does_not_replay_post(self):
        RequestHandler.status = 500
        with self.assertRaises(urllib.error.HTTPError) as error:
            self.opener.open(self.url, data=b"credentials", timeout=5)
        error.exception.close()
        self.assertEqual(RequestHandler.requests, 1)


class RetryDeadlineTests(unittest.TestCase):
    def test_connection_retry_deadline_reports_failure(self):
        with (
            mock.patch.object(stork_management.time, "monotonic", side_effect=[0, 0, 61]),
            mock.patch.object(socket, "create_connection", side_effect=TimeoutError()),
            self.assertRaisesRegex(TimeoutError, "Could not connect to Stork"),
        ):
            stork_management.connect_with_retry("stork", 443)


if __name__ == "__main__":
    unittest.main()
