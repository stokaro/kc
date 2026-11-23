package appconfig_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	yaml "sigs.k8s.io/yaml"

	"github.com/stokaro/kc/pkg/appconfig"
)

func TestParseHotkey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    string
		wantErr string
	}{
		{in: "M-1", want: "M-1"},
		{in: "M-H", want: "M-H"},
		{in: "M-h", want: "M-h"},
		{in: "M--", want: "M--"},
		{in: "M-C-x", want: "C-M-x"},
		{in: "F11", want: "F11"},
		{in: "f12", want: "F12"},
		{in: "C-x 1", want: "C-x 1"},
		{in: "  C-x   C-K ", want: "C-x C-k"},
		{in: "", wantErr: "empty"},
		{in: "x", wantErr: "no modifier"},
		{in: "C-k", wantErr: "belongs to kc"},
		{in: "F9", wantErr: "function key"},
		{in: "M-F3", wantErr: "modifiers on function keys"},
		{in: "M-xy", wantErr: "one character"},
		{in: "M-", wantErr: "one character"},
		{in: "C-1", wantErr: "C- takes a letter"},
		{in: "C-y 1", wantErr: "starts with C-x"},
		{in: "C-x M-1", wantErr: "after C-x"},
		{in: "C-x 1 2", wantErr: "more than two keys"},
		{in: "Alt+1", wantErr: "one character"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, err := appconfig.ParseHotkey(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ParseHotkey(%q) err = %v, want it to contain %q", tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ParseHotkey(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestValidateCommandsHotkeys(t *testing.T) {
	t.Parallel()
	cmds := []appconfig.CommandConfig{
		{Name: "one", Command: "echo 1", Type: appconfig.CommandTypeGlobal, Hotkey: "M-1"},
		{Name: "dup", Command: "echo 2", Type: appconfig.CommandTypeGlobal, Hotkey: "M-1"},
		{Name: "search", Command: "echo 3", Type: appconfig.CommandTypeGlobal, Hotkey: "M-s"},
		{Name: "compare", Command: "echo 4", Type: appconfig.CommandTypeGlobal, Hotkey: "C-x C-d"},
		{Name: "bad", Command: "echo 5", Type: appconfig.CommandTypeGlobal, Hotkey: "Ctrl+5"},
		{Name: "chord", Command: "echo 6", Type: appconfig.CommandTypeGlobal, Hotkey: "C-x  2"},
	}
	keys, err := appconfig.ValidateCommands(cmds)
	want := map[string]int{"M-1": 0, "C-x 2": 5}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("hotkeys = %v, want %v", keys, want)
	}
	if cmds[5].Hotkey != "C-x  2" {
		t.Errorf("ValidateCommands must not change the commands, hotkey is %q", cmds[5].Hotkey)
	}
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		`commands[1] "dup": hotkey M-1 is already bound to "one"`,
		`commands[2] "search": hotkey M-s is kc's key for quick search`,
		`commands[3] "compare": hotkey C-x C-d is kc's key for compare manifests`,
		`commands[4] "bad": hotkey "Ctrl+5"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), `"one"`+":") || strings.Contains(err.Error(), `"chord"`) {
		t.Errorf("valid commands must not be reported:\n%v", err)
	}
}

// The keys of the node actions (#114) and of the JSON logs viewer (#112)
// are kc's own; a user hotkey on them is ignored.
func TestValidateCommandsRefusesLaterBuiltinKeys(t *testing.T) {
	t.Parallel()
	for key, what := range map[string]string{
		"M-n": "node actions",
		"M-v": "raw or formatted logs",
		"M-g": "log filter",
		"M-A": "copy original log records",
	} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			cmds := []appconfig.CommandConfig{{Name: "mine", Command: "echo", Type: appconfig.CommandTypeGlobal, Hotkey: key}}
			keys, err := appconfig.ValidateCommands(cmds)
			if len(keys) != 0 {
				t.Fatalf("hotkeys = %v, want none", keys)
			}
			if want := "hotkey " + key + " is kc's key for " + what; err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want it to contain %q", err, want)
			}
		})
	}
}

func TestValidateCommandsPromptsAndConfirm(t *testing.T) {
	t.Parallel()
	prompt := func(name string) appconfig.CommandPrompt { return appconfig.CommandPrompt{Name: name} }
	tests := []struct {
		name    string
		cmd     appconfig.CommandConfig
		wantErr string
	}{
		{name: "valid", cmd: appconfig.CommandConfig{Command: "scale --replicas={{.Input.n}}", Prompts: []appconfig.CommandPrompt{prompt("n")}}},
		{name: "valid confirm", cmd: appconfig.CommandConfig{Command: "rm", Type: appconfig.CommandTypeSticky, Confirm: appconfig.CommandConfirm{Enabled: true, Message: "Delete {{.Input.n}}?"}, Prompts: []appconfig.CommandPrompt{prompt("n")}}},
		{name: "unknown prompt", cmd: appconfig.CommandConfig{Command: "echo {{.Input.n}}"}, wantErr: `command reads .Input.n but there is no prompt "n"`},
		{name: "unknown prompt in message", cmd: appconfig.CommandConfig{Command: "x", Confirm: appconfig.CommandConfirm{Enabled: true, Message: "{{.Input.m}}?"}}, wantErr: "confirm reads .Input.m"},
		{name: "bad name", cmd: appconfig.CommandConfig{Command: "x", Prompts: []appconfig.CommandPrompt{prompt("a-b")}}, wantErr: "template identifier"},
		{name: "duplicate name", cmd: appconfig.CommandConfig{Command: "x", Prompts: []appconfig.CommandPrompt{prompt("a"), prompt("a")}}, wantErr: "used twice"},
		{name: "default not a choice", cmd: appconfig.CommandConfig{Command: "x", Prompts: []appconfig.CommandPrompt{{Name: "a", Default: "z", Choices: []string{"x", "y"}}}}, wantErr: "not one of the choices"},
		{name: "too many", cmd: appconfig.CommandConfig{Command: "x", Prompts: make([]appconfig.CommandPrompt, appconfig.MaxCommandPrompts+1)}, wantErr: "at most"},
		{name: "broken template", cmd: appconfig.CommandConfig{Command: "{{.Name"}, wantErr: "parsing command template"},
		{name: "confirm on namespace command", cmd: appconfig.CommandConfig{Command: "x", Type: appconfig.CommandTypeNamespace, Confirm: appconfig.CommandConfirm{Enabled: true}}, wantErr: "type namespace"},
		{name: "confirm with watch", cmd: appconfig.CommandConfig{Command: "x", WatchInterval: metav1.Duration{Duration: time.Second}, Confirm: appconfig.CommandConfirm{Enabled: true}}, wantErr: "watchInterval"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.cmd.Name = tc.name
			tc.cmd.Hotkey = "M-9"
			keys, err := appconfig.ValidateCommands([]appconfig.CommandConfig{tc.cmd})
			if tc.wantErr == "" {
				if err != nil || appconfig.ValidateCommand(tc.cmd) != nil || len(keys) != 1 {
					t.Fatalf("hotkeys %v, err %v; want the command valid and bound", keys, err)
				}
				return
			}
			if len(keys) != 0 {
				t.Fatalf("a command that cannot run must not get a hotkey")
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "does not run") {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if err := appconfig.ValidateCommand(tc.cmd); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateCommand err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestCommandConfirmYAML(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		yaml string
		want appconfig.CommandConfirm
		out  string // marshaled back, "" when omitted
	}{
		{name: "absent", yaml: "name: a", out: ""},
		{name: "true", yaml: "confirm: true", want: appconfig.CommandConfirm{Enabled: true}, out: "confirm: true"},
		{name: "false", yaml: "confirm: false", out: ""},
		{name: "message", yaml: `confirm: "Delete {{.Name}}?"`, want: appconfig.CommandConfirm{Enabled: true, Message: "Delete {{.Name}}?"}, out: "confirm: Delete {{.Name}}?"},
		{name: "empty message", yaml: `confirm: ""`, out: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var cmd appconfig.CommandConfig
			if err := yaml.Unmarshal([]byte(tc.yaml), &cmd); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if cmd.Confirm != tc.want {
				t.Fatalf("confirm = %+v, want %+v", cmd.Confirm, tc.want)
			}
			data, err := yaml.Marshal(cmd)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			has := strings.Contains(string(data), "confirm:")
			switch {
			case tc.out == "" && has:
				t.Fatalf("unset confirm must be omitted:\n%s", data)
			case tc.out != "" && !strings.Contains(string(data), tc.out):
				t.Fatalf("marshaled %q, want it to contain %q", data, tc.out)
			}
		})
	}
	var cmd appconfig.CommandConfig
	if err := yaml.Unmarshal([]byte("confirm: 3"), &cmd); err == nil {
		t.Fatal("a number must be rejected")
	}
}

func TestDefaultCommandsValidate(t *testing.T) {
	t.Parallel()
	cmds := appconfig.Default().Commands
	if _, err := appconfig.ValidateCommands(cmds); err != nil {
		t.Fatalf("default commands must validate: %v", err)
	}
}

// TestReadmeExampleValidates keeps the README example of hotkeys, prompts
// and confirmation valid.
func TestReadmeExampleValidates(t *testing.T) {
	t.Parallel()
	const example = `
commands:
  - name: "Scale"
    command: "kubectl scale {{.Resource}} {{.Name}} -n {{.Namespace}} --replicas={{.Input.replicas}}"
    type: "sticky"
    showFor:
      resources: ["deployments", "statefulsets"]
    hotkey: "M-1"
    prompts:
      - name: replicas
        label: "Replicas"
        default: "1"
      - name: wait
        choices: ["no", "yes"]
    confirm: "Scale {{.Name}} to {{.Input.replicas}}?"
`
	var cfg appconfig.Config
	if err := yaml.UnmarshalStrict([]byte(example), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	keys, err := appconfig.ValidateCommands(cfg.Commands)
	if err != nil || keys["M-1"] != 0 || len(keys) != 1 {
		t.Fatalf("keys %v, err %v", keys, err)
	}
	if c := cfg.Commands[0].Confirm; !c.Enabled || c.Message == "" {
		t.Fatalf("confirm = %+v", c)
	}
}

func TestValidateDiagnosticCommands(t *testing.T) {
	t.Parallel()
	krr := func(mut func(*appconfig.CommandConfig)) appconfig.CommandConfig {
		c := appconfig.CommandConfig{
			Name: "KRR", Type: appconfig.CommandTypeSticky,
			Diagnostic: &appconfig.DiagnosticConfig{Adapter: "krr", Args: []string{"krr", "simple", "-f", "json", "-n", "{{.Namespace}}"}},
		}
		if mut != nil {
			mut(&c)
		}
		return c
	}
	tests := []struct {
		name    string
		cmd     appconfig.CommandConfig
		wantErr string
	}{
		{name: "valid", cmd: krr(nil)},
		{name: "prompt in args", cmd: krr(func(c *appconfig.CommandConfig) {
			c.Prompts = []appconfig.CommandPrompt{{Name: "days"}}
			c.Diagnostic.Args = append(c.Diagnostic.Args, "--history_duration={{.Input.days}}")
		})},
		{name: "unknown adapter", cmd: krr(func(c *appconfig.CommandConfig) { c.Diagnostic.Adapter = "gadget" }), wantErr: `adapter "gadget" is unknown; kc has krr`},
		{name: "no args", cmd: krr(func(c *appconfig.CommandConfig) { c.Diagnostic.Args = nil }), wantErr: "args must start with the executable"},
		{name: "templated executable", cmd: krr(func(c *appconfig.CommandConfig) { c.Diagnostic.Args[0] = "{{.Name}}" }), wantErr: "cannot be a template"},
		{name: "with command", cmd: krr(func(c *appconfig.CommandConfig) { c.Command = "krr simple" }), wantErr: "leave command empty"},
		{name: "interactive", cmd: krr(func(c *appconfig.CommandConfig) { c.Interactive = true }), wantErr: "interactive does not apply"},
		{name: "watch", cmd: krr(func(c *appconfig.CommandConfig) { c.WatchInterval = metav1.Duration{Duration: time.Second} }), wantErr: "runs once"},
		{name: "namespace type", cmd: krr(func(c *appconfig.CommandConfig) { c.Type = appconfig.CommandTypeNamespace }), wantErr: "use sticky or global"},
		{name: "negative timeout", cmd: krr(func(c *appconfig.CommandConfig) { c.Diagnostic.Timeout = metav1.Duration{Duration: -time.Second} }), wantErr: "negative"},
		{name: "unknown prompt in args", cmd: krr(func(c *appconfig.CommandConfig) {
			c.Diagnostic.Args = append(c.Diagnostic.Args, "{{.Input.nope}}")
		}), wantErr: `diagnostic.args[6] reads .Input.nope but there is no prompt "nope"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := appconfig.ValidateCommand(tc.cmd)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestDiagnosticTimeoutDefault(t *testing.T) {
	t.Parallel()
	if got := (appconfig.DiagnosticConfig{}).EffectiveTimeout(); got != appconfig.DefaultDiagnosticTimeout {
		t.Fatalf("default timeout = %s", got)
	}
	if got := (appconfig.DiagnosticConfig{Timeout: metav1.Duration{Duration: time.Minute}}).EffectiveTimeout(); got != time.Minute {
		t.Fatalf("timeout = %s", got)
	}
}

// TestReadmeDiagnosticExampleValidates keeps the README KRR example valid.
func TestReadmeDiagnosticExampleValidates(t *testing.T) {
	t.Parallel()
	const example = `
commands:
  - name: "KRR recommendations"
    type: "sticky"
    showFor:
      resources: ["deployments", "statefulsets", "daemonsets", "namespaces"]
    hotkey: "C-x k"
    diagnostic:
      adapter: krr
      args: ["krr", "simple", "-f", "json", "--logtostderr", "-n", "{{.Namespace}}"]
      timeout: 10m
`
	var cfg appconfig.Config
	if err := yaml.UnmarshalStrict([]byte(example), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, err := appconfig.ValidateCommands(cfg.Commands); err != nil {
		t.Fatal(err)
	}
	d := cfg.Commands[0].Diagnostic
	if d == nil || d.Adapter != "krr" || d.EffectiveTimeout() != 10*time.Minute || len(d.Args) != 7 {
		t.Fatalf("diagnostic = %+v", d)
	}
}
