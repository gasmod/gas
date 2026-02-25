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
}

func (a *App) logRouteMapDev(services []string, routes map[string][]RegisteredRoute) {
	named := a.router.NamedMiddleware()

	lines, sep := formatRouteTable(services, routes)

	var b strings.Builder
	b.WriteString(lines)

	if len(named) > 0 {
		writeNamedMiddlewareDev(&b, named, sep)
	}

	a.getLogger().Info(b.String()).Send()
}

func formatRouteTable(services []string, routes map[string][]RegisteredRoute) (table, separator string) {
	// ── Compute column widths ───────────────────────────────────
	maxMethod, maxPath := 0, 0
	total := 0
	for _, svc := range services {
		for _, rt := range routes[svc] {
			if len(rt.Method) > maxMethod {
				maxMethod = len(rt.Method)
			}
			if len(rt.Path) > maxPath {
				maxPath = len(rt.Path)
			}
		}
		total += len(routes[svc])
	}

	rowFmt := fmt.Sprintf("      %%-%ds  %%-%ds", maxMethod, maxPath)

	// ── Build route map ─────────────────────────────────────────
	maxLineWidth := 0
	type entry struct {
		header string
		lines  []string
	}
	entries := make([]entry, 0, len(services))

	for _, svc := range services {
		header := "  [" + svc + "]:"
		if len(header) > maxLineWidth {
			maxLineWidth = len(header)
		}
		e := entry{header: header}
		for _, rt := range routes[svc] {
			line := fmt.Sprintf(rowFmt, rt.Method, rt.Path)
			if len(rt.Middleware) > 0 {
				line += "  [" + strings.Join(rt.Middleware, ", ") + "]"
			}
			if len(line) > maxLineWidth {
				maxLineWidth = len(line)
			}
			e.lines = append(e.lines, line)
		}
		entries = append(entries, e)
	}

	sep := strings.Repeat("─", maxLineWidth)

	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "route map (%d routes across %d services):", total, len(services))
	b.WriteString("\n" + sep)
	for _, e := range entries {
		b.WriteString("\n" + e.header)
		for _, line := range e.lines {
			b.WriteString("\n" + line)
		}
	}
	b.WriteString("\n" + sep)

	return b.String(), sep
}

func writeNamedMiddlewareDev(b *strings.Builder, named map[string]string, sep string) {
	// Group named middleware by the service that registered them.
	svcMiddleware := make(map[string][]string)
	for mw, svc := range named {
		svcMiddleware[svc] = append(svcMiddleware[svc], mw)
	}

	mwServices := make([]string, 0, len(svcMiddleware))
	for svc := range svcMiddleware {
		mwServices = append(mwServices, svc)
	}
	sort.Strings(mwServices)

	b.WriteString("\n  named middleware:\n")
	for _, svc := range mwServices {
		b.WriteString("\n  [" + svc + "]:")
		sort.Strings(svcMiddleware[svc])
		for _, mw := range svcMiddleware[svc] {
			b.WriteString("\n      " + mw)
		}
	}

	b.WriteString("\n" + sep)
}

func (a *App) logRouteMapProd(services []string, routes map[string][]RegisteredRoute) {
	named := a.router.NamedMiddleware()

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

	if len(named) == 0 {
		return
	}

	// Group named middleware by the service that registered them.
	svcMiddleware := make(map[string][]string)
	for mw, svc := range named {
		svcMiddleware[svc] = append(svcMiddleware[svc], mw)
	}

	mwServices := make([]string, 0, len(svcMiddleware))
	for svc := range svcMiddleware {
		mwServices = append(mwServices, svc)
	}
	sort.Strings(mwServices)

	for _, svc := range mwServices {
		sort.Strings(svcMiddleware[svc])
		for _, mw := range svcMiddleware[svc] {
			a.getLogger().Info("named middleware").
				Str("service", svc).
				Str("alias", mw).
				Str("handler", named[mw]).
				Send()
		}
	}
}
