package dir

// The constructor follows the CONVENTIONS.md shape: the returned function
// returns the value and an error.
var _ func() (*Store, error) = NewStore("")
