package main

import (
	"testing"

	"github.com/alecthomas/kong"
	"github.com/sttts/kc/internal/ui"
)

func TestReadOnlyFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    ui.ReadOnlyOverride
		wantErr bool
	}{
		{name: "no flag leaves it to the config", args: nil, want: ui.ReadOnlyFromConfig},
		{name: "--readonly", args: []string{"--readonly"}, want: ui.ReadOnlyForced},
		{name: "--write", args: []string{"--write"}, want: ui.ReadOnlyWriteForced},
		{name: "--readonly with a subcommand", args: []string{"--readonly", "get", "pods"}, want: ui.ReadOnlyForced},
		{name: "both are refused", args: []string{"--readonly", "--write"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cli cliFlags
			parser, err := kong.New(&cli, kong.Name("kc"), kong.Exit(func(int) {}))
			if err != nil {
				t.Fatal(err)
			}
			_, err = parser.Parse(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("parse succeeded, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := readOnlyOverride(&cli); got != tt.want {
				t.Fatalf("readOnlyOverride = %v, want %v", got, tt.want)
			}
		})
	}
}
