package gas

import (
	"fmt"
	"sort"
	"strings"
)

func (a *App) logRouteMap(isDev bool) {
	routes := a.router.Routes()
	if len(routes) == 0 {
		return
	}

	// Sort service names for deterministic output.
	services := make([]string, 0, len(routes))
	for svc := range routes {
		services = append(services, svc)
	}
	sort.Strings(services)

	if isDev {
		a.logRouteMapDev(services, routes)
	} else {
		a.logRouteMapProd(services, routes)
	}

	a.logNamedMiddleware(isDev)
}

func (a *App) logRouteMapDev(services []string, routes map[string][]RegisteredRoute) {
	// Compute column widths for alignment.
	maxMethod, maxPath, maxService := 0, 0, 0
	for _, svc := range services {
		for _, rt := range routes[svc] {
			if len(rt.Method) > maxMethod {
				maxMethod = len(rt.Method)
			}
			if len(rt.Path) > maxPath {
				maxPath = len(rt.Path)
			}
			if len(svc) > maxService {
				maxService = len(svc)
			}
		}
	}

	// Count total routes.
	total := 0
	for _, svc := range services {
		total += len(routes[svc])
	}

	rowFmt := fmt.Sprintf("    %%-%ds  %%-%ds", maxMethod, maxPath)
	sep := strings.Repeat("─", maxMethod+maxPath+maxService+8)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("route map (%d routes across %d services):", total, len(services)))
	b.WriteString("\n" + sep)
	for _, svc := range services {
		b.WriteString("\n  " + svc + ":")
		for _, rt := range routes[svc] {
			line := fmt.Sprintf(rowFmt, rt.Method, rt.Path)
			if len(rt.Middleware) > 0 {
				line += "  [" + strings.Join(rt.Middleware, ", ") + "]"
			}
			b.WriteString("\n" + line)
		}
	}
	b.WriteString("\n" + sep)

	a.getLogger().Info(b.String()).Send()
}

func (a *App) logRouteMapProd(services []string, routes map[string][]RegisteredRoute) {
	for _, svc := range services {
		for _, rt := range routes[svc] {
			e := a.getLogger().Info("route registered").
				Str("service", svc).
				Str("method", rt.Method).
				Str("path", rt.Path)
			if len(rt.Middleware) > 0 {
				e = e.Str("middleware", strings.Join(rt.Middleware, ", "))
			}
			e.Send()
		}
	}
}

func (a *App) logNamedMiddleware(isDev bool) {
	named := a.router.NamedMiddleware()
	if len(named) == 0 {
		return
	}

	names := make([]string, 0, len(named))
	for name := range named {
		names = append(names, name)
	}
	sort.Strings(names)

	if isDev {
		maxName := 0
		for _, name := range names {
			if len(name) > maxName {
				maxName = len(name)
			}
		}
		aliasFmt := fmt.Sprintf("    %%-%ds → %%s", maxName)

		var b strings.Builder
		b.WriteString("named middleware:")
		for _, name := range names {
			b.WriteString("\n" + fmt.Sprintf(aliasFmt, name, named[name]))
		}
		a.getLogger().Info(b.String()).Send()
	} else {
		for _, name := range names {
			a.getLogger().Info("named middleware").
				Str("alias", name).
				Str("handler", named[name]).
				Send()
		}
	}
}
