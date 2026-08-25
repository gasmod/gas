package env

import "strings"

// unsafeEnvVars contains environment variables that should be filtered out for security.
var unsafeEnvVars = map[string]bool{
	// User/session info
	"PATH": true,
	"HOME": true,
	// Dynamic linker (code-execution vectors)
	"LD_PRELOAD":            true,
	"LD_LIBRARY_PATH":       true,
	"DYLD_INSERT_LIBRARIES": true,
	"DYLD_LIBRARY_PATH":     true,
	"USER":                  true,
	"USERNAME":              true,
	"LOGNAME":               true,
	"SHELL":                 true,
	"PWD":                   true,
	"OLDPWD":                true,
	"MAIL":                  true,

	// Locale/terminal
	"TERM":      true,
	"LANG":      true,
	"LC_ALL":    true,
	"LC_CTYPE":  true,
	"COLORTERM": true,

	// Temp/session dirs
	"TMPDIR": true,
	"TMP":    true,
	"TEMP":   true,

	// Privilege escalation
	"SUDO_USER": true,
	"SUDO_UID":  true,
	"SUDO_GID":  true,

	// SSH/remote session
	"SSH_AUTH_SOCK":  true,
	"SSH_AGENT_PID":  true,
	"SSH_CLIENT":     true,
	"SSH_CONNECTION": true,
	"SSH_TTY":        true,

	// Desktop/session vars
	"DISPLAY":         true,
	"XAUTHORITY":      true,
	"WAYLAND_DISPLAY": true,

	// Runtime/system info
	"HOSTNAME":        true,
	"HOST":            true,
	"COMPUTERNAME":    true,
	"SESSION_MANAGER": true,

	// Shell framework / customization
	"ZSH":    true,
	"BASH":   true,
	"PROMPT": true,
	"PS1":    true,
	"PS2":    true,

	// Editors & misc tools
	"EDITOR": true,
	"VISUAL": true,
	"PAGER":  true,

	// CI/CD common variables (can leak secrets if blindly loaded)
	"GITHUB_ACTION": true,
	"GITHUB_TOKEN":  true,
	"CI":            true,
}

// IsUnsafeVar checks if an environment variable should be filtered out.
func IsUnsafeVar(key string) bool {
	return unsafeEnvVars[strings.ToUpper(key)]
}

// BuildNestedMap creates a nested map structure from a key with separators.
func BuildNestedMap(data map[string]any, key string, value any, separator string) {
	if separator == "" {
		data[key] = value

		return
	}

	parts := strings.Split(key, separator)
	current := data

	for i, part := range parts {
		if i == len(parts)-1 {
			current[part] = value
		} else {
			if _, exists := current[part]; !exists {
				current[part] = make(map[string]any)
			}

			if nested, ok := current[part].(map[string]any); ok {
				current = nested
			} else {
				current[part] = make(map[string]any)
				current = current[part].(map[string]any)
			}
		}
	}
}
