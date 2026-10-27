package webaddr

import (
	"strings"
	"testing"
)

func TestCandidates(t *testing.T) {
	for in, want := range map[string][]string{
		"cloud.example.com":           {"https://cloud.example.com"},
		" https://drive.example.com/": {"https://drive.example.com"},
		"http://localhost:9200":       {"http://localhost:9200"},
		"192.168.0.10:9200":           {"https://192.168.0.10:9200", "http://192.168.0.10:9200"},
		"nas.local/cloud/":            {"https://nas.local/cloud", "http://nas.local/cloud"},
		"nas":                         {"https://nas", "http://nas"},
	} {
		got, err := Candidates(in)
		if err != nil || strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("Candidates(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "  ", "ftp://x.example.com"} {
		if _, err := Candidates(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
