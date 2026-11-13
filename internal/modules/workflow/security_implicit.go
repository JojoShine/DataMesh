package workflow

import (
	"archive/zip"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"io"
	"strings"
)

const securityTracePart = "customXml/datamesh-delivery-trace.xml"

type securityTraceXML struct {
	XMLName xml.Name `xml:"urn:datamesh:delivery:1 trace"`
	Token   string   `xml:",chardata"`
}

func securityTraceToken(proof SecurityDeliveryProof, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("datamesh-content-trace-v1\x00" + proof.ID + "\x00" + proof.ActorID + "\x00" + proof.KeyID + "\x00" + string(securityCanonical(proof.Resource))))
	return "dm1." + proof.ID + "." + hex.EncodeToString(mac.Sum(nil))
}
func securityTraceID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "dm1" || len(parts[1]) != 32 || len(parts[2]) != 64 {
		return "", invalid
	}
	if _, err := hex.DecodeString(parts[1]); err != nil {
		return "", invalid
	}
	if _, err := hex.DecodeString(parts[2]); err != nil {
		return "", invalid
	}
	return parts[1], nil
}

// The trace is a recoverable package marker; editing/resaving may remove it.
// Its HMAC binds the identifier to the signed delivery record, not to ownership.
func securityTraceExcel(body []byte, token string) ([]byte, error) {
	if _, err := securityTraceID(token); err != nil {
		return nil, err
	}
	if err := validateOffice(body, ".xlsx"); err != nil {
		return nil, err
	}
	source, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, invalid
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, file := range source.File {
		if strings.HasPrefix(strings.ToLower(file.Name), "_xmlsignatures/") {
			return nil, problem(409, "SECURITY_SIGNED_FILE_UNSUPPORTED", "带数字签名的文件不能重新保存为溯源副本")
		}
		if file.Name == securityTracePart {
			return nil, problem(409, "SECURITY_TRACE_EXISTS", "文件已含交付溯源标识，请使用原始文件生成新副本")
		}
		if err = writer.Copy(file); err != nil {
			return nil, err
		}
	}
	entry, err := writer.Create(securityTracePart)
	if err != nil {
		return nil, err
	}
	payload, err := xml.Marshal(securityTraceXML{Token: token})
	if err != nil {
		return nil, err
	}
	if _, err = entry.Write(payload); err != nil {
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	if output.Len() > 64<<20 {
		return nil, problem(413, "SECURITY_FILE_LIMIT", "溯源副本超过64MiB上限")
	}
	return output.Bytes(), nil
}
func securityExtractExcelTrace(body []byte) (string, error) {
	if err := validateOffice(body, ".xlsx"); err != nil {
		return "", err
	}
	source, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", invalid
	}
	token := ""
	for _, file := range source.File {
		if file.Name != securityTracePart {
			continue
		}
		if token != "" || file.UncompressedSize64 > 2048 {
			return "", invalid
		}
		stream, err := file.Open()
		if err != nil {
			return "", err
		}
		raw, readErr := io.ReadAll(io.LimitReader(stream, 2049))
		closeErr := stream.Close()
		if readErr != nil {
			return "", readErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		if len(raw) > 2048 {
			return "", invalid
		}
		var marker securityTraceXML
		if xml.Unmarshal(raw, &marker) != nil {
			return "", invalid
		}
		if _, err = securityTraceID(marker.Token); err != nil {
			return "", err
		}
		token = marker.Token
	}
	if token == "" {
		return "", problem(404, "SECURITY_TRACE_MISSING", "未找到可恢复的交付溯源标识")
	}
	return token, nil
}
func securityTraceJSON(body []byte, token string) ([]byte, error) {
	if _, err := securityTraceID(token); err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || object == nil {
		return nil, problem(409, "SECURITY_TRACE_FORMAT_UNSUPPORTED", "此响应格式不支持包内溯源标识")
	}
	if _, exists := object["_datamesh_trace"]; exists {
		return nil, problem(409, "SECURITY_TRACE_EXISTS", "响应已含保留的溯源字段")
	}
	object["_datamesh_trace"] = encode(token)
	return json.Marshal(object)
}
