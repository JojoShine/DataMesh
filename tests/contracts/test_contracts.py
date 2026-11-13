import copy
import json
import unittest
from pathlib import Path

from jsonschema import Draft202012Validator, FormatChecker

ROOT = Path(__file__).resolve().parents[2]


def load(path):
    return json.loads((ROOT / path).read_text())


class Contracts(unittest.TestCase):
    def setUp(self):
        self.validator = Draft202012Validator(
            load("contracts/events/event.schema.json"), format_checker=FormatChecker()
        )
        self.update = load("contracts/examples/postgres-update.json")

    def test_schema_and_all_positive_examples(self):
        Draft202012Validator.check_schema(self.validator.schema)
        for name in ["postgres-update", "delete", "api-arrival-order"]:
            self.validator.validate(load(f"contracts/examples/{name}.json"))

    def test_missing_keys_and_versions_are_rejected(self):
        for field in ["key", "schema_version", "source_position", "run_id"]:
            with self.subTest(field=field):
                event = copy.deepcopy(self.update)
                del event[field]
                self.assertFalse(self.validator.is_valid(event))
        self.assertFalse(self.validator.is_valid(dict(self.update, key={})))

    def test_delete_requires_no_business_fields_but_has_no_after_row(self):
        event = load("contracts/examples/delete.json")
        self.validator.validate(event)
        self.assertFalse(self.validator.is_valid(dict(event, after={"id": 1})))

    def test_unknown_source_order_requires_declared_arrival_semantics(self):
        self.assertFalse(self.validator.is_valid(dict(self.update, source_order=None)))
        self.validator.validate(dict(self.update, source_order=None, ordering_guarantee="per_key_arrival"))

    def test_bad_event_boundaries_are_rejected(self):
        for change in [{"captured_at": "yesterday"}, {"op": "upsert"}, {"schema_version": 0}, {"after": None}]:
            self.assertFalse(self.validator.is_valid(dict(self.update, **change)))

    def test_resource_layers_are_distinct_and_strict(self):
        schema = load("contracts/resources/resource.schema.json")
        validator = Draft202012Validator(schema)
        raw = {"resource_type": "raw_object", "resource_id": "same-id", "contract_version": 1}
        governed = dict(raw, resource_type="governed_dataset")
        validator.validate(raw)
        validator.validate(governed)
        self.assertNotEqual(raw, governed)
        self.assertFalse(validator.is_valid(dict(raw, resource_type="dataset")))
        self.assertFalse(validator.is_valid({"resource_id": "same-id", "contract_version": 1}))

    def test_grant_never_omits_layer_or_fields(self):
        validator = Draft202012Validator(load("contracts/resources/grant.schema.json"), format_checker=FormatChecker())
        grant = load("contracts/examples/raw-grant.json")
        validator.validate(grant)
        wrong = copy.deepcopy(grant)
        del wrong["resource"]["resource_type"]
        self.assertFalse(validator.is_valid(wrong))
        self.assertFalse(validator.is_valid(dict(grant, fields=[])))

    def test_states_have_no_dangling_transitions(self):
        for name in ["task", "operation"]:
            state = load("contracts/states.yaml")[name]
            self.assertIn(state["initial"], state["transitions"])
            for targets in state["transitions"].values():
                for target in targets:
                    self.assertIn(target, state["transitions"])

    def test_directory_draft_and_unknown_fields(self):
        validator=Draft202012Validator(load("contracts/resources/directory.schema.json"))
        draft={"name":"待编目资源","classification":"department","format":"database","data_layer":"raw_object","sharing_type":"conditional"}
        validator.validate(draft)
        self.assertFalse(validator.is_valid(dict(draft, source_password="must-not-be-a-directory-field")))
        self.assertFalse(validator.is_valid(dict(draft, sharing_type="public")))

    def test_mount_contract_preserves_physical_resource_identity(self):
        validator=Draft202012Validator(load("contracts/resources/mount.schema.json"))
        request={"directory_id":"directory","resource":{"resource_type":"governed_dataset","resource_id":"dataset","contract_version":1},"mapping":{"id":"id"}}
        validator.validate(request)
        self.assertFalse(validator.is_valid(dict(request,mapping={})))
        self.assertFalse(validator.is_valid(dict(request,resource={"resource_id":"dataset","contract_version":1})))

    def test_openapi_references_and_security(self):
        api = load("api/openapi/platform.yaml")
        schemas = api["components"]["schemas"]
        identifiers = set()
        def visit(value):
            if isinstance(value, dict):
                if "$ref" in value:
                    self.assertTrue(value["$ref"].startswith("#/components/schemas/"))
                    self.assertIn(value["$ref"].split("/")[-1], schemas)
                for child in value.values():
                    visit(child)
            elif isinstance(value, list):
                for child in value:
                    visit(child)
        visit(api)
        for path, methods in api["paths"].items():
            for method, operation in methods.items():
                self.assertNotIn(operation["operationId"], identifiers)
                identifiers.add(operation["operationId"])
                if path.startswith("/api/public/assets") or path in ("/auth/test-accounts", "/healthz"):
                    self.assertEqual(method,"get")
                    self.assertEqual(operation["security"],[])
                elif not path.startswith(("/health/", "/auth/login")):
                    self.assertTrue(operation["security"])
                if method == "post" and operation["security"] == [{"session": []}]:
                    self.assertIn("X-CSRF-Token", [p["name"] for p in operation["parameters"]])

    def test_explicit_full_experience_response_contract(self):
        api = load("api/openapi/platform.yaml")
        schema = api["paths"]["/auth/test-accounts"]["get"]["responses"]["200"]["content"]["application/json"]["schema"]
        validator = Draft202012Validator(schema)
        account = {"username": "experience_viewer", "password": "deliberately-public-test-only", "label": "真实业务体验", "description": "正常授权流程", "mode": "full"}
        validator.validate({"items": [account]})
        self.assertFalse(validator.is_valid({"items": [dict(account, mode="admin")]}))
        me = {"user_id": "experience-user", "username": "experience_viewer", "department_id": "experience", "platforms": ["governance"], "roles": ["governor"], "csrf_token": "x" * 43, "public_test": False, "full_experience": True}
        principal = Draft202012Validator(api["components"]["schemas"]["Me"])
        principal.validate(me)
        self.assertFalse(principal.is_valid(dict(me, full_experience="true")))

    def test_bulk_exchange_delivery_contracts(self):
        api = load("api/openapi/platform.yaml")
        for path in ["/api/v1/datasets/{id}/result-binding", "/api/v1/etl-runs/{id}/reconciliation", "/api/v1/etl-runs/{id}/receipts"]:
            self.assertIn(path, api["paths"])
            self.assertIn("post", api["paths"][path])
            self.assertIn("get", api["paths"][path])
        schemas = api["components"]["schemas"]
        self.assertIn("input_snapshot", schemas["ETLRunData"]["properties"])
        self.assertIn("timings", schemas["ETLRunData"]["properties"])
        self.assertEqual(schemas["DeliveryReceiptInput"]["properties"]["decision"]["enum"], ["accepted", "rejected"])
        self.assertIn("duration_ms", schemas["DeliveryReconciliationReport"]["properties"])


if __name__ == "__main__":
    unittest.main()
