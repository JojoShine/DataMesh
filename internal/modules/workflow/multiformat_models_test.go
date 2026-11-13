package workflow

import (
	"datamesh/internal/modules/identity"
	"testing"
)

func TestDeliveryMethodIsolation(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		method DeliveryMethod
	}{{"raw_object", DeliveryQuery}, {"governed_dataset", DeliveryQuery}, {"table_resource", DeliveryQuery}, {"file_resource", DeliveryDownload}, {"api_resource", DeliveryInvoke}} {
		if resourceMethod(Resource{Type: tc.kind}) != tc.method {
			t.Fatal(tc.kind)
		}
		for _, method := range []DeliveryMethod{DeliveryQuery, DeliveryDownload, DeliveryInvoke} {
			if method != tc.method {
				_, _, _, _, err := (&Server{}).authorizeDelivery(nil, identity.Principal{}, Resource{Type: tc.kind}, method, nil)
				if err == nil {
					t.Fatalf("%s accepted %s", tc.kind, method)
				}
			}
		}
	}
	if grantMethod(GrantRequest{}) != DeliveryQuery {
		t.Fatal("legacy scope changed")
	}
	if !validResourceFields(Resource{Type: "file_resource"}, nil, nil) || validResourceFields(Resource{Type: "table_resource"}, nil, nil) {
		t.Fatal("file scope leaked into query")
	}
	if validResourceFields(Resource{Type: "file_resource"}, []string{"id"}, nil) {
		t.Fatal("file must be whole version")
	}
}

func TestSourceAllowlistDoesNotExpandCDCTemplate(t *testing.T) {
	t.Setenv("SOURCE_ALLOWED_TARGETS", `[{"kind":"postgres","host":"approved.internal","port":5432,"database":"reporting"}]`)
	source := SourceInput{Kind: "postgres", Host: "approved.internal", Port: 5432, Database: "reporting"}
	if !endpointAllowed(source) || localSourceTemplate(source) {
		t.Fatal("table allowlist changed CDC boundary")
	}
	source.Host = "other.internal"
	if endpointAllowed(source) {
		t.Fatal("unapproved source allowed")
	}
}

func TestRemoteSourceAllowlistKinds(t *testing.T) {
	t.Setenv("SOURCE_ALLOWED_TARGETS", `[{"kind":"postgres","host":"db.internal","port":5432,"database":"business"},{"kind":"mysql","host":"db.internal","port":3306,"database":"business"},{"kind":"oracle","host":"db.internal","port":1521,"database":"ORCLPDB1"}]`)
	for _, v := range []SourceInput{{Kind: "postgres", Host: "db.internal", Port: 5432, Database: "business"}, {Kind: "mysql", Host: "db.internal", Port: 3306, Database: "business"}, {Kind: "oracle", Host: "db.internal", Port: 1521, Database: "ORCLPDB1"}} {
		if !endpointAllowed(v) || localSourceTemplate(v) {
			t.Fatal("remote database should be allowed without enabling sample CDC")
		}
		v.Database = "unauthorized"
		if endpointAllowed(v) {
			t.Fatal("database scope widened")
		}
	}
}
