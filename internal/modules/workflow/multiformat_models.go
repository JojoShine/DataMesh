package workflow

import "encoding/json"

type DeliveryMethod string

const (
	DeliveryQuery    DeliveryMethod = "query"
	DeliveryDownload DeliveryMethod = "download"
	DeliveryInvoke   DeliveryMethod = "invoke"
)

func resourceMethod(ref Resource) DeliveryMethod {
	switch ref.Type {
	case "file_resource":
		return DeliveryDownload
	case "api_resource":
		return DeliveryInvoke
	default:
		return DeliveryQuery
	}
}
func grantMethod(v GrantRequest) DeliveryMethod {
	if v.Materials == nil || v.Materials.Method == "" {
		return DeliveryQuery
	}
	return DeliveryMethod(v.Materials.Method)
}
func validResourceFields(ref Resource, fields []string, schema []Field) bool {
	if ref.Type == "file_resource" {
		return len(fields) == 0
	}
	return validFields(fields, schema)
}
func extendedResource(kind string) bool {
	return kind == "table_resource" || kind == "file_resource" || kind == "api_resource"
}

// Versions are immutable; replacing a target creates a new resource and mount revision.
type ManagedResource struct {
	Name      string          `json:"name"`
	DataLayer string          `json:"data_layer"`
	Fields    []Field         `json:"fields"`
	Adapter   string          `json:"adapter"`
	Config    json.RawMessage `json:"config"`
}
