package services

import (
	"regexp"
	"testing"
)

func TestPatternRegex(t *testing.T) {
	cases := []struct {
		pattern string
		match   map[string]string // reference -> captured counter ("" = must not match)
	}{
		{"EO-DIR-{YYYY}-{NNN}", map[string]string{
			"EO-DIR-2026-001":   "001",
			"EO-DIR-2026-1000":  "1000",
			"EO-DIR-2025-001":   "",
			"EO-DIR-2026-001-P": "",
			"EO-DIR-2026-01":    "",
		}},
		{"TSK-{YYYY}-{NNNNN}", map[string]string{
			"TSK-2026-00014": "00014",
			"TSK-2026-0014":  "",
		}},
		{"MED.{NNN}", map[string]string{ // "." is literal, not "any character"
			"MED.007": "007",
			"MEDX007": "",
		}},
		{"{parent}-X{NN}", map[string]string{
			"EO-DIR-2026-001-X05": "05",
		}},
	}
	for _, c := range cases {
		re := regexp.MustCompile(patternRegex(c.pattern, 2026))
		for ref, want := range c.match {
			m := re.FindStringSubmatch(ref)
			got := ""
			if m != nil {
				got = m[1]
			}
			if got != want {
				t.Errorf("pattern %q, ref %q: got %q, want %q (regex %s)", c.pattern, ref, got, want, re)
			}
		}
	}
	if r := patternRegex("{parent}-A{#}", 2026); r != "" {
		t.Errorf("a pattern without a counter should give no regex, got %q", r)
	}
}
