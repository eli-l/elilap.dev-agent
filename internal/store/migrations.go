package store

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// listMigrations returns the sorted *.sql migration filenames in dir,
// rejecting anything that is not a .sql file so a stray editor backup can
// not silently join the sequence.
func listMigrations(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		if strings.ContainsAny(e.Name(), " ~") || strings.HasPrefix(e.Name(), ".") {
			return nil, fmt.Errorf("migrations dir %s: unexpected file name %q", dir, e.Name())
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}
