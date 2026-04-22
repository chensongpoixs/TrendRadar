package storage

import "testing"

func TestNormalizeURLString(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{
			"https://Example.COM/a/../c?utm_source=x&gclid=1",
			"https://example.com/c",
		},
		{
			"https://example.com:443/path",
			"https://example.com/path",
		},
		{
			"http://example.com:80/",
			"http://example.com/",
		},
	}
	for _, c := range cases {
		got := normalizeURLString(c.raw)
		if got != c.want {
			t.Errorf("normalizeURLString(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}
