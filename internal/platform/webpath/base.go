package webpath

import (
	"fmt"
	"regexp"
	"strings"
)

var basePattern = regexp.MustCompile(`^(?:/[A-Za-z0-9_-]+)+$`)

func Normalize(base string) (string, error) {
	if base == "" || base == "/" {
		return "", nil
	}
	base = strings.TrimSuffix(base, "/")
	if len(base) > 128 || !basePattern.MatchString(base) {
		return "", fmt.Errorf("invalid BASE_PATH")
	}
	return base, nil
}
