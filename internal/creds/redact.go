package creds

import (
	"os"
	"sort"
	"strings"
)

// minRedactLen avoids redacting trivially short values that would mangle
// unrelated text.
const minRedactLen = 4

// Redact replaces every occurrence of each value in s with [REDACTED:NAME].
// Longer values are replaced first so overlapping secrets redact cleanly.
func Redact(s string, values map[string]string) string {
	if len(values) == 0 || s == "" {
		return s
	}
	type kv struct{ name, value string }
	var list []kv
	for k, v := range values {
		if len(v) >= minRedactLen {
			list = append(list, kv{k, v})
		}
	}
	sort.Slice(list, func(i, j int) bool { return len(list[i].value) > len(list[j].value) })
	for _, e := range list {
		s = strings.ReplaceAll(s, e.value, "[REDACTED:"+e.name+"]")
	}
	return s
}

// RedactFile rewrites a file with values redacted. A missing file is fine.
func RedactFile(path string, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	out := Redact(string(b), values)
	if out == string(b) {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(out), fi.Mode().Perm())
}
