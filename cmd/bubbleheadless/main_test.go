package main

import "testing"

func TestParseKeyTokenHex(t *testing.T) {
	tests := []struct {
		token   string
		want    string
		wantErr bool
	}{
		{token: "0x41", want: "A"},
		{token: "0x1f600", want: "\U0001F600"},
		{token: "0x110000", wantErr: true},    // past the last code point
		{token: "0xd800", wantErr: true},      // surrogate
		{token: "0x100000041", wantErr: true}, // would wrap to "A" as a rune
	}
	for _, tt := range tests {
		t.Run(tt.token, func(t *testing.T) {
			got, err := parseKeyToken(tt.token)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
