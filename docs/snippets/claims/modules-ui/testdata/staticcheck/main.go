// Command staticcheck starts an app with a ui service outside go test, so
// the static directory check runs. With no argument the ui defaults apply;
// "dir=<path>" sets ui.staticdir and "fs" passes a static filesystem. It
// prints "started" or the start error.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing/fstest"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/ui"
)

func main() {
	values := map[string]any{}
	var opts []ui.Option
	for _, arg := range os.Args[1:] {
		switch {
		case arg == "fs":
			opts = append(opts, ui.WithStaticFS(fstest.MapFS{}))
		case strings.HasPrefix(arg, "dir="):
			values["ui"] = map[string]any{"staticdir": strings.TrimPrefix(arg, "dir=")}
		}
	}
	cfg, err := configtest.NewMockConfigWithValues(values)
	if err != nil {
		fmt.Println(err)
		return
	}
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.TemplateProvider](memory.NewStore()),
		gas.WithSingletonService[gas.UIProvider](ui.New[gas.TemplateProvider](opts...)),
	)
	if err := app.Start(context.Background()); err != nil {
		fmt.Println(err)
		return
	}
	_ = app.Stop(context.Background())
	fmt.Println("started")
}
