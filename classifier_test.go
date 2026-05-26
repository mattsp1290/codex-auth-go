package codexauth

import (
	"errors"
	"testing"
)

type apiError struct {
	Code    any
	Message string
}

func (e *apiError) Error() string { return e.Message }

func TestClassifyCodexError_PlanNotIncluded(t *testing.T) {
	cause := &apiError{Code: "usage_not_included", Message: "plan does not include Codex"}
	err := ClassifyCodexError(cause)

	if !errors.Is(err, ErrPlanNotIncluded) {
		t.Fatalf("errors.Is(err, ErrPlanNotIncluded) = false; err = %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is(err, cause) = false; err = %v", err)
	}
	if errors.Is(err, ErrQuotaExceeded) {
		t.Fatal("plan error unexpectedly matches ErrQuotaExceeded")
	}
}

func TestClassifyCodexError_QuotaExceeded(t *testing.T) {
	cause := &apiError{Code: "insufficient_quota", Message: "quota exceeded"}
	err := ClassifyCodexError(cause)

	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("errors.Is(err, ErrQuotaExceeded) = false; err = %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is(err, cause) = false; err = %v", err)
	}
	if errors.Is(err, ErrPlanNotIncluded) {
		t.Fatal("quota error unexpectedly matches ErrPlanNotIncluded")
	}
}

func TestClassifyCodexError_Unknown(t *testing.T) {
	cause := &apiError{Code: "server_error", Message: "internal server error"}
	err := ClassifyCodexError(cause)

	if errors.Is(err, ErrPlanNotIncluded) {
		t.Fatal("unknown error unexpectedly matches ErrPlanNotIncluded")
	}
	if errors.Is(err, ErrQuotaExceeded) {
		t.Fatal("unknown error unexpectedly matches ErrQuotaExceeded")
	}
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is(err, cause) = false; err = %v", err)
	}
}

func TestClassifyCodexError_WrappedCode(t *testing.T) {
	cause := &apiError{Code: "usage_not_included", Message: "plan does not include Codex"}
	err := ClassifyCodexError(errors.Join(errors.New("outer"), cause))

	if !errors.Is(err, ErrPlanNotIncluded) {
		t.Fatalf("errors.Is(err, ErrPlanNotIncluded) = false; err = %v", err)
	}
}
