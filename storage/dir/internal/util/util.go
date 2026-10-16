package util

import (
	"errors"
	"fmt"
	"io"
)

// CloseWithErr closes every closer, in order, and returns err joined with
// each close error. It returns nil only when err is nil and every close
// succeeded.
func CloseWithErr(err error, closers ...io.Closer) error {
	for _, c := range closers {
		if e := c.Close(); e != nil {
			err = errors.Join(err, fmt.Errorf("close: %w", e))
		}
	}
	return err
}
