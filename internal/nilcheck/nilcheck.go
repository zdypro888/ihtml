// Package nilcheck contains the SDK's interface-boundary nil guard.
package nilcheck

import "reflect"

// IsNil reports whether value is nil either directly or through an interface
// that contains a nil pointer/map/slice/function/channel/interface. It is meant
// for public dependency-injection boundaries, not ordinary concrete values.
func IsNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
