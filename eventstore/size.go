package eventstore

import (
	"errors"
	"fmt"
	"strings"
)

// MaxPublicationBytes leaves room under SNS's default 256 KiB body+attributes
// ceiling. Generated subscriptions use raw delivery; configure receiving queues
// for at least 256 KiB. This bounds individual events, not batches or snapshots.
const MaxPublicationBytes = 240 << 10

func ValidatePublicationSize(publication Publication) error {
	body, err := Encode(publication)
	if err != nil {
		return errors.Join(ErrInvalid, err)
	}
	if len(body) > MaxPublicationBytes {
		return fmt.Errorf("%w: publication exceeds %d bytes", ErrInvalid, MaxPublicationBytes)
	}
	return nil
}

// Check before hashing with maximum-width generated fields, so root counters,
// hashes and later retry bookkeeping cannot make an accepted event unpublishable.
func validateInputSize(in Input) error {
	const maximumExactInteger int64 = 9007199254740991
	hash := strings.Repeat("0", 64)
	return ValidatePublicationSize(Publication{ID: hash, Attempts: maximumExactInteger, Event: Event{Input: in, Number: maximumExactInteger, Hash: hash, Fingerprint: hash}})
}
