package migrate

import (
	"runtime/debug"
	"strings"
	"sync"
)

const ownModulePath = "github.com/gasmod/gas/migrate"

// buildInfo caches the parsed build info on first access.
var (
	buildInfoOnce sync.Once
	depVersions   map[string]string // service path → version
	selfVersion   string
)

func loadBuildInfo() {
	buildInfoOnce.Do(func() {
		depVersions = make(map[string]string)
		info, ok := debug.ReadBuildInfo()
		if !ok {
			return
		}
		for _, dep := range info.Deps {
			path := dep.Path
			if dep.Replace != nil {
				path = dep.Replace.Path
			}
			ver := dep.Version
			if dep.Replace != nil {
				ver = dep.Replace.Version
			}
			depVersions[path] = ver
		}
		// If we are the main service (unlikely but possible in tests).
		if info.Main.Path == ownModulePath {
			selfVersion = info.Main.Version
			return
		}
		selfVersion = depVersions[ownModulePath]
	})
}

// migrateVersion returns the version of gas/migrate itself.
func migrateVersion() string {
	loadBuildInfo()
	return selfVersion
}

// resolveModuleVersion attempts to find the Go service version for a Gas
// service name (e.g. "gas/auth"). It searches build info deps for a path
// ending with the service name. Returns empty string if not found.
func resolveModuleVersion(moduleName string) string {
	loadBuildInfo()
	for path, ver := range depVersions {
		if strings.HasSuffix(path, "/"+moduleName) || path == moduleName {
			return ver
		}
	}
	return ""
}
