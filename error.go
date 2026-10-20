package lazy

import "errors"

var ErrNotFound = errors.New("not found")

// errLoadPanicked is the error of a load which panicked (see Cache.loadEntry)
var errLoadPanicked = errors.New("LoadOneFunc panicked")
