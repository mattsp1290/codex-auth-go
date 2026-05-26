package codexauth

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
)

const (
	codexErrCodeUsageNotIncluded  = "usage_not_included"
	codexErrCodeInsufficientQuota = "insufficient_quota"
)

// ClassifyCodexError maps Codex API error codes to exported auth sentinels.
//
// Errors with a Code field matching "usage_not_included" wrap
// ErrPlanNotIncluded; errors with "insufficient_quota" wrap ErrQuotaExceeded.
// Unrecognised errors are wrapped as generic Codex generation failures.
func ClassifyCodexError(err error) error {
	if err == nil {
		return nil
	}

	switch code, ok := findErrorCode(err); {
	case ok && code == codexErrCodeUsageNotIncluded:
		return &classifiedError{sentinel: ErrPlanNotIncluded, cause: err}
	case ok && code == codexErrCodeInsufficientQuota:
		return &classifiedError{sentinel: ErrQuotaExceeded, cause: err}
	default:
		return fmt.Errorf("codex generate: %w", err)
	}
}

type classifiedError struct {
	sentinel error
	cause    error
}

func (e *classifiedError) Error() string {
	if e == nil || e.cause == nil {
		return e.sentinel.Error()
	}
	return e.cause.Error()
}

func (e *classifiedError) Unwrap() []error {
	if e == nil || e.cause == nil {
		return []error{e.sentinel}
	}
	return []error{e.sentinel, e.cause}
}

func findErrorCode(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	if code, ok := codeField(err); ok {
		return code, true
	}
	var multi interface{ Unwrap() []error }
	if errors.As(err, &multi) {
		for _, child := range multi.Unwrap() {
			if code, ok := findErrorCode(child); ok {
				return code, true
			}
		}
		return "", false
	}
	var single interface{ Unwrap() error }
	if errors.As(err, &single) {
		return findErrorCode(single.Unwrap())
	}
	return "", false
}

func codeField(err error) (string, bool) {
	v := reflect.ValueOf(err)
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return "", false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return "", false
	}
	f := v.FieldByName("Code")
	if !f.IsValid() || !f.CanInterface() {
		return "", false
	}
	switch code := f.Interface().(type) {
	case string:
		return code, code != ""
	case int:
		return strconv.Itoa(code), true
	default:
		if f.IsZero() {
			return "", false
		}
		return fmt.Sprint(code), true
	}
}
