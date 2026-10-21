package engine

import "testing"

func TestParseAmount(t *testing.T) {
	good := map[string]Cents{"32.00": 3200, "32.5": 3250, "32": 3200, "0.05": 5, " 18.00 ": 1800}
	for s, want := range good {
		if got, err := ParseAmount(s); err != nil || got != want {
			t.Errorf("ParseAmount(%q) = %d, %v; want %d", s, got, err, want)
		}
	}
	for _, s := range []string{"", "-1.00", "1.005", "1.", ".50", "1e3", "$5", "1,000.00", "99999999999999999999"} {
		if _, err := ParseAmount(s); err == nil {
			t.Errorf("ParseAmount(%q) should fail", s)
		}
	}
}
