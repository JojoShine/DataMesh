import sys
from pathlib import Path
import unittest
from unittest.mock import patch
from urllib.error import HTTPError

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "scripts/smoke"))
import cdc


class ConnectorControl(unittest.TestCase):
    @patch("cdc.time.sleep")
    @patch("cdc.api")
    def test_status_propagation_does_not_create_twice(self, api, sleep):
        config = {"connector.class": "fixture"}
        api.side_effect = [[], {"name": "sample"}, config,
                           HTTPError("local", 404, "status not propagated", {}, None),
                           {"connector": {"state": "RUNNING"}, "tasks": [{"state": "RUNNING"}]}]
        cdc.ensure_connector("sample", config)
        posts = [call for call in api.call_args_list if call.args[0] == "POST"]
        self.assertEqual(len(posts), 1)
        sleep.assert_called_once()

    @patch("cdc.api")
    def test_existing_different_configuration_is_not_overwritten(self, api):
        api.side_effect = [["sample"], {"database.password": "different-private-value"}]
        with self.assertRaises(RuntimeError) as caught:
            cdc.ensure_connector("sample", {"database.password": "expected-private-value"})
        self.assertNotIn("private-value", str(caught.exception))
        self.assertFalse(any(call.args[0] == "POST" for call in api.call_args_list))

    @patch("cdc.time.monotonic", side_effect=[0, 91])
    @patch("cdc.api", return_value=["sample"])
    def test_wait_is_bounded_without_resubmission(self, api, monotonic):
        with self.assertRaisesRegex(RuntimeError, "90 seconds"):
            cdc.ensure_connector("sample", {})
        api.assert_called_once_with("GET", "/connectors")


if __name__ == "__main__":
    unittest.main()
