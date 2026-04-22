package ai

import "testing"

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
		err  bool
	}{
		{
			name: "pure JSON array",
			raw:  `[{"index":0,"score":0.9}]`,
			want: `[{"index":0,"score":0.9}]`,
		},
		{
			name: "markdown fence",
			raw:  "```json\n[{\"index\":0}]\n```",
			want: `[{"index":0}]`,
		},
		{
			name: "text before JSON",
			raw:  "好的，这是结果：\n[{\"a\":1}]",
			want: `[{"a":1}]`,
		},
		{
			name: "text after JSON",
			raw:  "[{\"a\":1}]\n以上就是分析。",
			want: `[{"a":1}]`,
		},
		{
			name: "nested object",
			raw:  `一些说明 {"core":"value","nested":{"k":"v"}} 结尾`,
			want: `{"core":"value","nested":{"k":"v"}}`,
		},
		{
			name: "empty",
			raw:  "",
			err:  true,
		},
		{
			name: "no JSON at all",
			raw:  "这只是一段文字，没有 JSON",
			err:  true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := extractJSON(c.raw)
			if c.err {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
