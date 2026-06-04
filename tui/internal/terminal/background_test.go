package terminal

import "testing"

func TestParseBackgroundColorResponse(t *testing.T) {
	tests := []struct {
		name string
		data string
		want RGB
		ok   bool
	}{
		{
			name: "rgb response terminated by ST",
			data: Escape + "]11;rgb:ffff/ffff/ffff" + Escape + "\\",
			want: RGB{R: 255, G: 255, B: 255},
			ok:   true,
		},
		{
			name: "rgb response terminated by BEL",
			data: Escape + "]11;rgb:0000/0000/0000\a",
			want: RGB{R: 0, G: 0, B: 0},
			ok:   true,
		},
		{
			name: "short rgb components scale to bytes",
			data: Escape + "]11;rgb:f/f/f" + Escape + "\\",
			want: RGB{R: 255, G: 255, B: 255},
			ok:   true,
		},
		{
			name: "hex response",
			data: Escape + "]11;#212529" + Escape + "\\",
			want: RGB{R: 33, G: 37, B: 41},
			ok:   true,
		},
		{
			name: "ignores incomplete response",
			data: Escape + "]11;rgb:ffff/ffff/ffff",
			ok:   false,
		},
		{
			name: "ignores malformed response",
			data: Escape + "]11;nope" + Escape + "\\",
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseBackgroundColorResponse([]byte(tt.data))
			if ok != tt.ok {
				t.Fatalf("ok\ngot:  %t\nwant: %t", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("color\ngot:  %#v\nwant: %#v", got, tt.want)
			}
		})
	}
}
