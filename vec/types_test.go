package vec_test

import (
	"strings"
	"testing"

	"github.com/go-again/sqlite/vec"
)

func TestParseMetric(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want vec.Metric
		err  bool
	}{
		{"", vec.L2, false},
		{"l2", vec.L2, false},
		{"L2", vec.L2, false},
		{"cosine", vec.Cosine, false},
		{"COSINE", vec.Cosine, false},
		{"dot", vec.Dot, false},
		{"l1", vec.Dot, false},
		{"unknown", 0, true},
		{"manhattan", 0, true},
	} {
		got, err := vec.ParseMetric(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("ParseMetric(%q) = (%v, nil), want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseMetric(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseMetric(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseMetric_KeywordRoundTrip(t *testing.T) {
	for _, m := range []vec.Metric{vec.L2, vec.Cosine, vec.Dot} {
		kw := m.Keyword()
		round, err := vec.ParseMetric(kw)
		if err != nil {
			t.Errorf("ParseMetric(%q from %v.Keyword()) error: %v", kw, m, err)
			continue
		}
		if round != m {
			t.Errorf("round-trip %v -> %q -> %v", m, kw, round)
		}
	}
}

func TestParseEncoding(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want vec.Encoding
		err  bool
	}{
		{"", vec.JSON, false},
		{"json", vec.JSON, false},
		{"JSON", vec.JSON, false},
		{"binary", vec.Binary, false},
		{"BINARY", vec.Binary, false},
		{"protobuf", 0, true},
	} {
		got, err := vec.ParseEncoding(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("ParseEncoding(%q) = (%v, nil), want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseEncoding(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseEncoding(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseMetric_ErrorMessage(t *testing.T) {
	_, err := vec.ParseMetric("manhattan")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "manhattan") {
		t.Errorf("error %q should mention the bad input", err.Error())
	}
}
