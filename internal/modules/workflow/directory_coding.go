package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func validateCodeTemplate(template string, width int) error {
	if template == "" {
		if width != 0 {
			return invalid
		}
		return nil
	}
	if len(template) > 200 || width < 1 || width > 12 || strings.Count(template, "{sequence}") != 1 || strings.Count(template, "{provider_code}") > 1 {
		return invalid
	}
	literal := strings.ReplaceAll(strings.ReplaceAll(template, "{sequence}", ""), "{provider_code}", "")
	if !regexp.MustCompile(`^[A-Za-z0-9._:/-]*$`).MatchString(literal) {
		return invalid
	}
	return nil
}

func (s *Server) registerDirectoryCoding(api, write *gin.RouterGroup) {
	write.POST("/directory-dictionaries/:id/coding", s.require("exchange"), s.mutatePath("configure_directory_coding", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		if err := requireResourceCapability(tx, p, Resource{}, "authority_review"); err != nil {
			return 0, nil, id, err
		}
		v, err := bind[struct {
			CodeTemplate    string `json:"code_template"`
			SequenceWidth   int    `json:"sequence_width"`
			ExpectedVersion int    `json:"expected_version"`
			Reason          string `json:"reason"`
		}](b)
		if err != nil {
			return 0, nil, id, err
		}
		if !valid(v.Reason, 1000) {
			return 0, nil, id, invalid
		}
		if err = validateCodeTemplate(v.CodeTemplate, v.SequenceWidth); err != nil {
			return 0, nil, id, problem(400, "CODE_TEMPLATE_INVALID", "模板必须包含一个 {sequence}，可包含 {provider_code}；序号位数为 1–12")
		}
		row, err := get(tx, "directory_dictionary", id, true)
		if err != nil {
			return 0, nil, id, err
		}
		if row.Version != v.ExpectedVersion || row.State != "active" {
			return 0, nil, id, conflict
		}
		dict, err := decode[DirectoryDictionary](row.Body)
		if err != nil {
			return 0, nil, id, err
		}
		if !dict.Verified {
			return 0, nil, id, problem(409, "DICTIONARY_NOT_VERIFIED", "请先核验编码字典")
		}
		if v.CodeTemplate != "" {
			pattern, e := regexp.Compile(dict.CodePattern)
			if e != nil {
				return 0, nil, id, e
			}
			sample := strings.ReplaceAll(v.CodeTemplate, "{sequence}", fmt.Sprintf("%0*d", v.SequenceWidth, 1))
			if !strings.Contains(sample, "{provider_code}") && !pattern.MatchString(sample) {
				return 0, nil, id, problem(400, "CODE_TEMPLATE_MISMATCH", "生成模板不符合当前字典的编码规则")
			}
		}
		before := row.Body
		dict.CodeTemplate, dict.SequenceWidth = v.CodeTemplate, v.SequenceWidth
		row.Body = encode(dict)
		row.Version++
		if err = save(tx, &row); err != nil {
			return 0, nil, id, err
		}
		if err = recordIdentityChange(tx, p, "directory_dictionary", id, v.Reason, before, row.Body); err != nil {
			return 0, nil, id, err
		}
		return 200, encode(gin.H{"id": id, "version": row.Version, "definition": dict}), id, nil
	}))
}

func fillDirectoryMetadata(tx *gorm.DB, dept, id string, v *DirectoryDefinition, newDirectory bool) error {
	unit, err := identity.ReadAffiliation(tx, dept)
	if err != nil && !identity.IsMissing(err) {
		return err
	}
	if err == nil {
		if !unit.Active {
			return problem(409, "UNIT_INACTIVE", "所属组织或部门已停用")
		}
		if unit.ProviderCode != "" {
			v.ProviderName, v.ProviderCode = unit.ProviderName, unit.ProviderCode
		} else if v.ProviderName == "" {
			v.ProviderName = unit.ProviderName
		}
	}
	if (v.ResourceCode == "" || newDirectory) && v.DictionaryID != "" {
		row, e := get(tx, "directory_dictionary", v.DictionaryID, true)
		if e != nil {
			return e
		}
		dict, e := decode[DirectoryDictionary](row.Body)
		if e != nil {
			return e
		}
		if dict.CodeTemplate != "" {
			if row.State != "active" || !dict.Verified || (dict.TestOnly && os.Getenv("DIRECTORY_TEST_MODE") != "1") {
				return problem(409, "DICTIONARY_NOT_VERIFIED", "编码字典未核验或不可用于当前环境")
			}
			if e = validateCodeTemplate(dict.CodeTemplate, dict.SequenceWidth); e != nil {
				return e
			}
			if strings.Contains(dict.CodeTemplate, "{provider_code}") && v.ProviderCode == "" {
				return problem(409, "PROVIDER_CODE_REQUIRED", "请管理员先补齐所属组织的正式单位代码")
			}
			pattern, e := regexp.Compile(dict.CodePattern)
			if e != nil {
				return e
			}
			for attempt := 0; attempt < 1000; attempt++ {
				var sequence int64
				if e = tx.Raw(`INSERT INTO directory_code_counters(dictionary_id,last_value) VALUES(?,1)
     ON CONFLICT(dictionary_id) DO UPDATE SET last_value=directory_code_counters.last_value+1 RETURNING last_value`, v.DictionaryID).Scan(&sequence).Error; e != nil {
					return e
				}
				digits := fmt.Sprintf("%0*d", dict.SequenceWidth, sequence)
				if len(digits) > dict.SequenceWidth {
					return problem(409, "CODE_SEQUENCE_EXHAUSTED", "编码序号已用尽，请主管调整编码规则")
				}
				code := strings.ReplaceAll(strings.ReplaceAll(dict.CodeTemplate, "{provider_code}", v.ProviderCode), "{sequence}", digits)
				if !pattern.MatchString(code) {
					return problem(400, "CODE_TEMPLATE_MISMATCH", "生成编码不符合字典规则，请主管检查模板和单位代码")
				}
				result := tx.Exec("INSERT INTO directory_code_reservations(code,directory_id,dictionary_id) VALUES(?,?,?) ON CONFLICT(code) DO NOTHING", code, id, v.DictionaryID)
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected == 1 {
					v.ResourceCode = code
					return nil
				}
			}
			return problem(409, "CODE_COLLISION_LIMIT", "编码已占用，请主管检查编码序号范围")
		}
	}
	if v.ResourceCode != "" {
		result := tx.Exec("INSERT INTO directory_code_reservations(code,directory_id,dictionary_id) VALUES(?,?,?) ON CONFLICT(code) DO NOTHING", v.ResourceCode, id, v.DictionaryID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var owner string
			if err = tx.Table("directory_code_reservations").Select("directory_id").Where("code=?", v.ResourceCode).Scan(&owner).Error; err != nil {
				return err
			}
			if owner != id {
				return problem(409, "RESOURCE_CODE_EXISTS", "资源编码已被其他目录使用")
			}
		}
	}
	return nil
}
