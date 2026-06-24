package film

import "testing"

func TestParsePageParams(t *testing.T) {
	cases := []struct {
		limStr, offStr   string
		wantLim, wantOff int
	}{
		{"", "", 60, 0},            // defaults
		{"20", "40", 20, 40},       // explicit
		{"0", "-5", 60, 0},         // non-positive limit & negative offset → defaults
		{"999", "10", 200, 10},     // limit capped at 200
		{"abc", "xyz", 60, 0},      // garbage → defaults
		{"60", "0", 60, 0},         // offset 0 stays 0
	}
	for _, c := range cases {
		lim, off := parsePageParams(c.limStr, c.offStr)
		if lim != c.wantLim || off != c.wantOff {
			t.Errorf("parsePageParams(%q,%q)=(%d,%d), want (%d,%d)", c.limStr, c.offStr, lim, off, c.wantLim, c.wantOff)
		}
	}
}
