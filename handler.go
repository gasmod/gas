package gas

import (
	"fmt"
	"net/http"
	"reflect"
)

// ErrorHandler converts a handler error into an HTTP response.
type ErrorHandler func(ctx Context, err error)

func defaultErrorHandler(ctx Context, err error) {
	if logger, err := ResolveFromRequestScope[Logger](ctx.Request()); err == nil {
		logger.Error("unhandled request error").Err("error", err).Send()
	}
	http.Error(ctx.ResponseWriter(), http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

// handlerMeta holds pre-computed reflection data for a DI-aware handler.
type handlerMeta struct {
	fn       reflect.Value
	depTypes []reflect.Type // param types excluding the first (Context)
}

// adaptHandler validates the handler signature and returns an http.HandlerFunc
// that resolves dependencies from the per-request scope at call time.
// It also returns the dependency types for boot-time validation.
//
// Valid signatures:
//
//	func(gas.Context) error
//	func(gas.Context, Dep1, Dep2, ...) error
//
// Panics if the signature is invalid.
func adaptHandler(handler any, errorHandler ErrorHandler) (http.HandlerFunc, []reflect.Type) {
	if errorHandler == nil {
		errorHandler = defaultErrorHandler
	}

	handlerVal := reflect.ValueOf(handler)
	handlerType := handlerVal.Type()

	if handlerType.Kind() != reflect.Func {
		panic(fmt.Errorf("gas: handler must be a function, got %T", handler))
	}

	if handlerType.NumIn() < 1 {
		panic(fmt.Errorf("gas: handler must accept gas.Context as first parameter, got 0 parameters"))
	}

	ctxType := reflect.TypeFor[Context]()
	if handlerType.In(0) != ctxType {
		panic(fmt.Errorf("gas: handler first parameter must be gas.Context, got %v", handlerType.In(0)))
	}

	errType := reflect.TypeFor[error]()
	if handlerType.NumOut() != 1 || !handlerType.Out(0).Implements(errType) {
		panic(fmt.Errorf("gas: handler must return exactly one value of type error, got %v", handlerType))
	}

	depTypes := make([]reflect.Type, handlerType.NumIn()-1)
	for i := 1; i < handlerType.NumIn(); i++ {
		depTypes[i-1] = handlerType.In(i)
	}

	meta := handlerMeta{
		fn:       handlerVal,
		depTypes: depTypes,
	}

	adapted := func(w http.ResponseWriter, r *http.Request) {
		ctx := NewContext(w, r)
		scope := RequestScope(r)

		args := make([]reflect.Value, 1+len(meta.depTypes))
		args[0] = reflect.ValueOf(ctx)

		for i, depType := range meta.depTypes {
			val, err := scope.resolveType(depType)
			if err != nil {
				errorHandler(ctx, fmt.Errorf("gas: resolving %v: %w", depType, err))
				return
			}
			args[i+1] = val
		}

		results := meta.fn.Call(args)

		if errVal := results[0]; !errVal.IsNil() {
			errorHandler(ctx, errVal.Interface().(error))
		}
	}

	return adapted, depTypes
}
