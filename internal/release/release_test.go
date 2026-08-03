package release

import "testing"

func TestSupportsMinimum(t *testing.T) {
	cases := []struct {
		current string
		minimum string
		want    bool
	}{
		{"1.2.0", "1.1.0", true},
		{"1.2.0", "1.2.0", true},
		{"1.1.9", "1.2.0", false},
		{"1.2.0-beta.1", "1.2.0", false},
		{"invalid", "1.2.0", false},
	}
	for _, item := range cases {
		if got := SupportsMinimum(item.current, item.minimum); got != item.want {
			t.Fatalf("SupportsMinimum(%q, %q) = %v, want %v", item.current, item.minimum, got, item.want)
		}
	}
}
