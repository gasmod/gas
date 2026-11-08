package env

import "strings"

// unsafeEnvVars contains environment variables that should be filtered out
// for security.
var unsafeEnvVars = map[string]bool{
	// User/session info
	"PATH": true,
	"HOME": true,
	// Dynamic linker (code-execution vectors)
	"LD_PRELOAD":                    true,
	"LD_LIBRARY_PATH":               true,
	"LD_AUDIT":                      true,
	"DYLD_INSERT_LIBRARIES":         true,
	"DYLD_LIBRARY_PATH":             true,
	"DYLD_FRAMEWORK_PATH":           true,
	"DYLD_FALLBACK_LIBRARY_PATH":    true,
	"DYLD_FALLBACK_FRAMEWORK_PATH":  true,
	"DYLD_VERSIONED_LIBRARY_PATH":   true,
	"DYLD_VERSIONED_FRAMEWORK_PATH": true,
	"USER":                          true,
	"USERNAME":                      true,
	"LOGNAME":                       true,
	"SHELL":                         true,
	"PWD":                           true,
	"OLDPWD":                        true,
	"MAIL":                          true,

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

	// Shell startup file, run by bash before a script (code-execution vector)
	"BASH_ENV": true,

	// Editors & misc tools
	"EDITOR": true,
	"VISUAL": true,
	"PAGER":  true,

	// CI/CD common variables (can leak secrets if blindly loaded)
	"GITHUB_ACTION": true,
	"GITHUB_TOKEN":  true,
	"CI":            true,
}

// IsUnsafeVar reports whether name is on the list of variables that
// describe the user, shell or session, matched without regard to case.
func IsUnsafeVar(name string) bool {
	return unsafeEnvVars[strings.ToUpper(name)]
}
