package workflow

import "testing"

func TestDirectoryCodeTemplate(t *testing.T) {
	for _, v := range []struct {
		template string
		width    int
		valid    bool
	}{
		{"", 0, true}, {"ORG-{sequence}", 6, true}, {"{provider_code}/{sequence}", 8, true},
		{"{sequence}-{sequence}", 6, false}, {"{unknown}-{sequence}", 6, false}, {"ORG", 6, false},
		{"ORG-{sequence}", 0, false}, {"ORG-{sequence}", 13, false}, {"", 6, false},
	} {
		if got := validateCodeTemplate(v.template, v.width) == nil; got != v.valid {
			t.Errorf("template=%q width=%d valid=%v", v.template, v.width, got)
		}
	}
}
