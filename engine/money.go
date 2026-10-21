package engine

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseAmount turns a decimal string such as "32.00", "32.5" or "32" into
// cents. It rejects negatives, more than two decimals and anything else.
func ParseAmount(s string) (Cents, error) {
	s = strings.TrimSpace(s)
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" || len(frac) > 2 || (hasDot && frac == "") || !digits(whole) || !digits(frac) {
		return 0, fmt.Errorf("amount %q must look like 32.00", s)
	}
	for len(frac) < 2 {
		frac += "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w > int64(MaxAmount/100) {
		return 0, fmt.Errorf("amount %q is too large", s)
	}
	f, _ := strconv.ParseInt(frac, 10, 64)
	return Cents(w*100 + f), nil
}

func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
