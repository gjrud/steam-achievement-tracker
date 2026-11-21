package version

import "testing"

func TestParseStamp(t *testing.T) {
	cases := []struct {
		name  string
		stamp string
		want  string
		ok    bool
	}{
		{"valid", "sat-version:0.3.0;", "0.3.0", true},
		{"valid prerelease", "sat-version:0.3.0-rc.1;", "0.3.0-rc.1", true},
		{"empty", "", "", false},
		{"only prefix", "sat-version:", "", false},
		{"no prefix", "0.3.0;", "", false},
		{"no terminator", "sat-version:0.3.0", "", false},
		{"empty version", "sat-version:;", "", false},
		{"double terminator", "sat-version:0.3.0;;", "", false},
		{"v prefix", "sat-version:v0.3.0;", "", false},
		{"partial minor", "sat-version:0.3;", "", false},
		{"partial major", "sat-version:0;", "", false},
		{"garbage", "sat-version:garbage;", "", false},
		{"leading text", "x sat-version:0.3.0;", "", false},
		{"build metadata", "sat-version:0.3.0+build;", "", false},
		{"leading zero", "sat-version:01.3.0;", "", false},
		{"empty prerelease", "sat-version:0.3.0-;", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseStamp(tc.stamp)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("ParseStamp(%q) = (%q, %v), want (%q, %v)", tc.stamp, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestCurrentUnstampedTestBinary(t *testing.T) {
	// The test binary is not built with -X, so tag is empty and the stamp is unusable.
	if got, ok := Current(); ok || got != "" {
		t.Fatalf("Current() = (%q, %v), want unstamped", got, ok)
	}
}

func TestIsPrerelease(t *testing.T) {
	if IsPrerelease("0.3.0") || !IsPrerelease("0.3.0-rc.1") {
		t.Fatal("IsPrerelease misclassified")
	}
}
