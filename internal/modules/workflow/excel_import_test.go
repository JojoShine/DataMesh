package workflow

import (
	"github.com/xuri/excelize/v2"
	"testing"
)

func workbookBytes(t *testing.T, rows [][]interface{}, formula bool) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	for i, row := range rows {
		axis, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := f.SetSheetRow("Sheet1", axis, &row); err != nil {
			t.Fatal(err)
		}
	}
	if formula {
		if err := f.SetCellFormula("Sheet1", "B2", "1+1"); err != nil {
			t.Fatal(err)
		}
	}
	b, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestExcelImportMappingAndPrecision(t *testing.T) {
	precision, scale := 24, 4
	def := DirectoryDefinition{Items: []DirectoryItem{{Name: "code", BusinessName: "编号", LogicalType: "string"}, {Name: "amount", BusinessName: "金额", LogicalType: "decimal", Precision: &precision, Scale: &scale}, {Name: "optional", BusinessName: "备注", LogicalType: "string", Nullable: true}}}
	data := workbookBytes(t, [][]interface{}{{"编号", "amount", "忽略列"}, {"001234567890123456789", "12345678901234567890.1234", "ignored"}}, false)
	preview, rows, err := parseExcel(data, def, ImportMapping{Sheet: "Sheet1", HeaderRow: 1})
	if err != nil || preview.ErrorCount != 0 || len(rows) != 1 {
		t.Fatalf("parse %v errors=%+v", err, preview.Errors)
	}
	if preview.Records[0]["code"] != "001234567890123456789" || preview.Records[0]["amount"] != "12345678901234567890.1234" || preview.Records[0]["optional"] != nil {
		t.Fatalf("precision or projection: %+v", preview.Records)
	}
	if len(preview.Ignored) != 1 || len(def.Items) != 3 {
		t.Fatal("mapping altered definition")
	}
	bad := workbookBytes(t, [][]interface{}{{"编号", "amount"}, {"1", "1.12345"}}, false)
	p, _, e := parseExcel(bad, def, ImportMapping{Sheet: "Sheet1", HeaderRow: 1})
	if e != nil || p.ErrorCount == 0 {
		t.Fatal("scale not checked")
	}
}
func TestExcelImportRejectsMissingAmbiguousAndFormula(t *testing.T) {
	def := DirectoryDefinition{Items: []DirectoryItem{{Name: "code", BusinessName: "编号", LogicalType: "string"}, {Name: "amount", BusinessName: "金额", LogicalType: "decimal"}}}
	for _, tc := range []struct {
		rows    [][]interface{}
		formula bool
	}{{[][]interface{}{{"code"}, {"abc"}}, false}, {[][]interface{}{{"code", "code"}, {"abc", "def"}}, false}, {[][]interface{}{{"code", "amount"}, {"abc", "2"}}, true}} {
		p, _, err := parseExcel(workbookBytes(t, tc.rows, tc.formula), def, ImportMapping{Sheet: "Sheet1", HeaderRow: 1})
		if err == nil && p.ErrorCount == 0 {
			t.Fatal("invalid workbook accepted")
		}
	}
}
func TestFileFormatValidation(t *testing.T) {
	for _, name := range []string{"payload.exe", "fake.xlsx", "fake.pdf", "file.zip"} {
		if _, e := validateSharedFile(name, []byte("not that format")); e == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	if _, e := validateSharedFile("说明.txt", []byte("真实文件内容")); e != nil {
		t.Fatal(e)
	}
}

func TestExcelDatesAndRequiredValues(t *testing.T) {
	def := DirectoryDefinition{Items: []DirectoryItem{{Name: "date", BusinessName: "日期", LogicalType: "date"}}}
	data := workbookBytes(t, [][]interface{}{{"date"}, {float64(45000)}}, false)
	preview, _, err := parseExcel(data, def, ImportMapping{Sheet: "Sheet1", HeaderRow: 1})
	if err != nil || preview.ErrorCount != 0 || preview.Records[0]["date"] != "2023-03-15" {
		t.Fatalf("date: %v %+v", err, preview)
	}
	if _, err = parseImportValue("", DirectoryItem{LogicalType: "integer"}); err == nil {
		t.Fatal("required blank accepted")
	}
}
