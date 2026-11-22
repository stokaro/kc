package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/stokaro/kc/internal/ui"
)

func parseGetIntent(args []string, output string) (*ui.GetIntent, error) {
	tokens, err := tokenizeGetArgs(args)
	if err != nil {
		return nil, err
	}
	format := strings.TrimSpace(strings.ToLower(output))
	if format != "" && format != "yaml" {
		return nil, fmt.Errorf("unsupported output format %q; supported formats: yaml", output)
	}
	return &ui.GetIntent{
		OutputFormat: format,
		Tokens:       tokens,
	}, nil
}

func tokenizeGetArgs(args []string) ([]ui.GetToken, error) {
	var tokens []ui.GetToken
	firstToken := true
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		parts := strings.Split(arg, ",")
		for _, part := range parts {
			value := strings.TrimSpace(part)
			if value == "" {
				continue
			}
			token := ui.GetToken{
				Original: value,
				Value:    value,
			}
			if strings.Contains(value, "/") {
				res, name, err := splitResourceName(value)
				if err != nil {
					return nil, err
				}
				token.Resource = res
				token.Name = name
				token.ExplicitResource = true
				token.ExplicitName = true
				token.FromSlash = true
			}
			if len(parts) > 1 {
				token.FromComma = true
				token.ExplicitResource = true
			}
			if firstToken && !token.ExplicitResource {
				token.ExplicitResource = true
			}
			tokens = append(tokens, token)
			firstToken = false
		}
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("kc get requires at least one resource or object")
	}
	return tokens, nil
}

func splitResourceName(value string) (resource, name string, err error) {
	parts := strings.SplitN(value, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid resource/name value %q", value)
	}
	resource = strings.TrimSpace(parts[0])
	name = strings.TrimSpace(parts[1])
	if resource == "" || name == "" {
		return "", "", fmt.Errorf("invalid resource/name value %q", value)
	}
	return resource, name, nil
}

// errAllNamespacesLogs rejects kc logs -A: a pod name is only unique in one
// namespace, and kubectl logs has no -A either.
var errAllNamespacesLogs = errors.New("kc logs does not accept -A/--all-namespaces; a pod name needs one namespace, use -n")

// checkAllNamespacesGet rejects object names with -A, as kubectl does: a
// name is only unique in one namespace. Only the first argument and
// comma lists name types; a TYPE/NAME pair or a later plain argument is a
// name.
func checkAllNamespacesGet(intent *ui.GetIntent) error {
	for _, tok := range intent.Tokens {
		if tok.ExplicitName || !tok.ExplicitResource {
			return fmt.Errorf("a resource cannot be retrieved by name across all namespaces: %q; drop -A or use -n", tok.Original)
		}
	}
	return nil
}

// newRunConfig maps the parsed flags to the UI startup options. ns is the
// namespace deriveStartupIntent chose.
func newRunConfig(cli *cliFlags, intent ui.StartupIntent, ns string) ui.RunConfig {
	return ui.RunConfig{
		Context:           strings.TrimSpace(cli.Context),
		Namespace:         ns,
		AllNamespaces:     cli.AllNamespaces,
		StartupIntent:     intent,
		ImpersonateUser:   strings.TrimSpace(cli.As),
		ImpersonateUID:    strings.TrimSpace(cli.AsUID),
		ImpersonateGroups: append([]string(nil), cli.AsGroup...),
	}
}
