package workflow

// Fixed portable patterns avoid accepting arbitrary expressions from a form.
func qualityFormatPattern(check Check) string {
	return map[string]string{
		"mobile":        `^1[3-9][0-9]{9}$`,
		"email":         `^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+[.][A-Za-z]{2,}$`,
		"date":          `^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])$`,
		"integer":       `^[+-]?[0-9]+$`,
		"decimal":       `^[+-]?[0-9]+([.][0-9]+)?$`,
		"no_whitespace": `^[^[:space:]]+$`,
	}[flowLiteralText(check.Parameters["format"])]
}
