package routing

import "testing"

// A bare string in an Xray routing rule is a substring match, while the same
// text in a geosite source file means a suffix match. Copying domains across
// without a prefix silently widens the rule, so every domain must come out
// explicitly prefixed.
func TestNormalizeValuesAlwaysPrefixesBareDomains(t *testing.T) {
	tests := []struct {
		name  string
		kind  string
		input string
		want  []string
	}{
		{"bare domain", "domain", "predict.fun", []string{"domain:predict.fun"}},
		{"strips www", "domain", "www.predict.fun", []string{"domain:predict.fun"}},
		{"uppercase folded", "domain", "Predict.FUN", []string{"domain:predict.fun"}},
		{"keeps full prefix", "domain", "full:a.b.com", []string{"full:a.b.com"}},
		{"keeps keyword prefix", "domain", "keyword:ads", []string{"keyword:ads"}},
		{"keeps regexp prefix", "domain", `regexp:^ads?[0-9]*\.`, []string{`regexp:^ads?[0-9]*\.`}},
		{"keeps geosite prefix", "domain", "geosite:cn", []string{"geosite:cn"}},
		{"keeps ext prefix", "domain", "ext:custom.dat:CRYPTO", []string{"ext:custom.dat:CRYPTO"}},
		{"ip values untouched", "ip", "1.1.1.1", []string{"1.1.1.1"}},
		{"cidr untouched", "ip", "10.0.0.0/8", []string{"10.0.0.0/8"}},
		{"geoip untouched", "ip", "geoip:cn", []string{"geoip:cn"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeValues(tt.kind, tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestNormalizeValuesSplitsOnEverySeparator(t *testing.T) {
	got := normalizeValues("domain", "a.com b.com,c.com\nd.com\t e.com")
	want := []string{"domain:a.com", "domain:b.com", "domain:c.com", "domain:d.com", "domain:e.com"}
	if len(got) != len(want) {
		t.Fatalf("got %d values %v, want %d", len(got), got, len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestNormalizeValuesDropsEmptyInput(t *testing.T) {
	if got := normalizeValues("domain", "  \n , \t "); len(got) != 0 {
		t.Fatalf("want no values, got %v", got)
	}
}
