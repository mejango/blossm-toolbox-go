package view

import (
	"errors"
	"fmt"
)

// ErrInvalid marks invalid caller input or an operation incompatible with a valid
// view body. It does not classify backend failures or trusted configuration.
// Transports may map errors.Is(err, ErrInvalid) to a client validation response.
var ErrInvalid = errors.New("invalid view input")

func invalid(err error) error {
	if err == nil || errors.Is(err, ErrInvalid) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrInvalid, err)
}
