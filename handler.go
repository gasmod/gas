package gas

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"runtime/debug"
)

// ErrorHandler converts a handler error into an HTTP response.
type ErrorHandler func(ctx Context, err error)

func defaultErrorHandler(ctx Context, err error) {
	if logger, resErr := ResolveFromRequestScope[Logger](ctx.Request()); resErr == nil {
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
func adaptHandler(handler any, getErrorHandler func() ErrorHandler) (http.HandlerFunc, []reflect.Type) {
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

	meta := &handlerMeta{fn: handlerVal, depTypes: depTypes}

	adapted := func(w http.ResponseWriter, r *http.Request) {
		// don't recover context initialization panics
		ctx := NewContext(r.Context(), w, r)

		defer func() {
			if rec := recover(); rec != nil {
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					// don't recover http.ErrAbortHandler
					panic(rec)
				}

				err := fmt.Errorf("gas: handler panic: %v", rec)

				debugStack := debug.Stack()

				// write to stderr regardless if we have a logger or not
				_, _ = os.Stderr.Write(debugStack)

				logger, resErr := ResolveFromRequestScope[Logger](r)
				if resErr == nil {
					logger.Error("handler panic").
						Err("panic", err).Str("stack", string(debugStack)).Send()
				}

				eh := getErrorHandler()
				eh(ctx, err)
			}
		}()

		scope := RequestScope(r)

		args := make([]reflect.Value, 1+len(meta.depTypes))
		args[0] = reflect.ValueOf(ctx)

		for i, depType := range meta.depTypes {
			val, err := scope.resolveType(depType)
			if err != nil {
				eh := getErrorHandler()
				eh(ctx, fmt.Errorf("gas: resolving %v: %w", depType, err))
				return
			}
			args[i+1] = val
		}

		results := meta.fn.Call(args)

		if errVal := results[0]; !errVal.IsNil() {
			eh := getErrorHandler()
			eh(ctx, errVal.Interface().(error))
		}
	}

	return adapted, depTypes
}
