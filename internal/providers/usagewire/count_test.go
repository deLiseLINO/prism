package usagewire

import "testing"

func TestExactIntegralCounts(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int64
	}{
		{"0", 0}, {"-0.0e9999999999999999999999", 0}, {"1.00", 1},
		{"100e-2", 1}, {"9.007199254740993e15", 9007199254740993},
		{"9223372036854775807", 9223372036854775807}, {"92233720368547758070e-1", 9223372036854775807},
		{"0.0000000000000000000000000000000000001e37", 1},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := Exact(tc.raw)
			if err != nil || got != tc.want {
				t.Fatalf("count=%d error=%v want=%d", got, err, tc.want)
			}
		})
	}
	for _, raw := range []string{"", "null", `"2"`, "true", "-1", "1.5", "1e-1", "9223372036854775808", "1e999999999999999999", "01", "+1", "1.", "1e", "1e+", "1 2"} {
		t.Run("invalid/"+raw, func(t *testing.T) {
			if _, err := Exact(raw); err == nil {
				t.Fatalf("accepted %q", raw)
			}
		})
	}
}
