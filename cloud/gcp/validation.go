package gcp

import (
	"context"
	"fmt"
	"reflect"
)

// Detect typed nil SDK pointers as well as an unconfigured interface.
func ready(ctx context.Context, client any) error {
	if ctx == nil {
		return fmt.Errorf("gcp: context required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if client == nil {
		return fmt.Errorf("gcp: client required")
	}
	v := reflect.ValueOf(client)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan:
		if v.IsNil() {
			return fmt.Errorf("gcp: client required")
		}
	}
	return nil
}
func ackIDs(ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("gcp: ack IDs required")
	}
	for _, id := range ids {
		if id == "" {
			return fmt.Errorf("gcp: empty ack ID")
		}
	}
	return nil
}
