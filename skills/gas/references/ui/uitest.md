# github.com/gasmod/gas/ui/uitest

```go
import "github.com/gasmod/gas/ui/uitest"
```

Package uitest holds a mock of ui.Provider, `MockUI`, for tests of code
that renders pages.

## Usage

The mock records each call, and returns what the function in the field
named after the method returns:

```go
m := &uitest.MockUI{
	RenderFn: func(_ context.Context, w http.ResponseWriter, name string, _ any, _ ...ui.RenderOption) error {
		_, err := io.WriteString(w, "<h1>"+name+"</h1>")
		return err
	},
}
err := m.Render(ctx, w, "home", nil, ui.WithStatus(http.StatusAccepted))
// w holds <h1>home</h1>, err is nil, and m.CallCount("Render") is 1
```

The mock keeps none of the rules of the ui package: it checks no name
or option and compiles nothing. To test against those rules, build a
ui.Service over a gas/template/memory store.

## Defaults

With a field nil, its method writes nothing to the response writer and
returns nil:

- Render and RenderFragment render nothing.
- RegisterFuncs accepts every map, nil and invalid functions included.

## Calls

Each method appends a `Call` with its name and its arguments after the
context and the response writer, before it calls its field. The
arguments recorded:

- Render and RenderFragment: the name, the data and the options. The
data is recorded as given. The options are resolved by
ui.ApplyRenderOptions into a ui.RenderOptions.
- RegisterFuncs: a copy of the function map, nil for nil.

To read the record:

- Calls returns a copy of the calls in the order they were made, with
copies of the function maps, so that changing what it returns
changes no recorded call.
- CallCount counts those of one method.
- Reset forgets them all.

The methods may be called from several goroutines at once, as the calls
are kept under a lock, which Calls, CallCount and Reset take too.
Setting a field while a method runs is a data race.

## API

Run `go doc -all github.com/gasmod/gas/ui/uitest` for every declaration, or `go doc github.com/gasmod/gas/ui/uitest.<Name>` for one.
