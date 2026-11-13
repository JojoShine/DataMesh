package migrations

import (
	"embed"
	"fmt"
	"strconv"
	"strings"
)

// Files binds database tasks and readiness to the migrations in this build.
//
//go:embed *.up.sql *.down.sql
var Files embed.FS

func Version() (uint, error) {
	entries, err := Files.ReadDir(".")
	if err != nil {
		return 0, err
	}
	versions := make(map[uint]uint)
	for _, entry := range entries {
		name := entry.Name()
		prefix, _, found := strings.Cut(name, "_")
		value, err := strconv.ParseUint(prefix, 10, 32)
		if !found || err != nil || value == 0 {
			return 0, fmt.Errorf("invalid embedded migration name")
		}
		version := uint(value)
		if strings.HasSuffix(name, ".up.sql") {
			versions[version] |= 1
		} else if strings.HasSuffix(name, ".down.sql") {
			versions[version] |= 2
		} else {
			return 0, fmt.Errorf("invalid embedded migration direction")
		}
	}
	for number := uint(1); number <= uint(len(versions)); number++ {
		if versions[number] != 3 {
			return 0, fmt.Errorf("incomplete embedded migrations")
		}
	}
	if len(versions) == 0 {
		return 0, fmt.Errorf("no embedded migrations")
	}
	return uint(len(versions)), nil
}
