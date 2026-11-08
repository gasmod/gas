# github.com/gasmod/gas/template/templatetest

```go
import "github.com/gasmod/gas/template/templatetest"
```

Package templatetest holds a mock of gas.TemplateProvider,
`MockTemplate`, for tests of code that reads or registers templates.

## Usage

The mock records each call, and returns what the function in the field
named after the method returns:

```go
m := &templatetest.MockTemplate{
	GetFn: func(context.Context, string) ([]byte, error) {
		return []byte("<p>hi</p>"), nil
	},
}
b, err := m.Get(ctx, "home.html")
// b is <p>hi</p>, err is nil, and m.CallCount("Get") is 1
```

## Defaults

With a field nil, its method behaves as an empty store:

- Get returns an error wrapping template.ErrTemplateNotFound,
`template: not found:` and the quoted name.
- List returns an empty, non-nil list.
- Register and RegisterFS return nil.

## Calls

Each method appends a `Call` with its name and its arguments after the
context, before it calls its field. The arguments recorded:

- Get: the name.
- List: none.
- Register: the name and the content. The content is recorded as a
copy, nil for nil, so that a caller that reuses its slice does not
change a recorded call.
- RegisterFS: the fs.FS.

To read the record:

- Calls returns a copy of the calls in the order they were made.
- CallCount counts those of one method.
- Reset forgets them all.

The methods may be called from several goroutines at once, as the calls
are kept under a lock, which Calls, CallCount and Reset take too.
Setting a field while a method runs is a data race.

## API

Run `go doc -all github.com/gasmod/gas/template/templatetest` for every declaration, or `go doc github.com/gasmod/gas/template/templatetest.<Name>` for one.
