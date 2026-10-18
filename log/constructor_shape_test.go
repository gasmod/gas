package log

// The logger constructors follow the CONVENTIONS.md shape: the returned
// function returns the value and an error.
var (
	_ func() (*ZeroLogLogger, error)  = NewZeroLogLogger()
	_ func() (*SlogLogger, error)     = NewSlogLogger()
	_ func() (*ShippingLogger, error) = NewShippingLogger("http://127.0.0.1:0", nil)
)
