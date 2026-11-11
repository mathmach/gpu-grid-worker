package derr_test

import (
	stderrors "errors"
	"fmt"
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
)

func TestCanonicalWorkerErrorCodesAreDistinctAndNonEmpty(t *testing.T) {
	codes := []derr.Code{
		derr.CodeValidation,
		derr.CodeInvalidCredentials,
		derr.CodeUnauthorized,
		derr.CodeForbidden,
		derr.CodeIllegalState,
		derr.CodeConflict,
		derr.CodeRateLimited,
		derr.CodeCircuitOpen,
		derr.CodeUpstreamFailure,
		derr.CodeNoVulkanGPU,
		derr.CodeDigestMismatch,
		derr.CodePathTraversal,
		derr.CodeUnsupportedModelFormat,
		derr.CodeMethodNotAllowed,
		derr.CodeNotFound,
		derr.CodeInternal,
	}
	seen := make(map[derr.Code]struct{}, len(codes))
	for _, code := range codes {
		if code == "" {
			t.Fatal("encountered empty error code constant")
		}
		if _, dup := seen[code]; dup {
			t.Fatalf("duplicate error code constant %q", code)
		}
		seen[code] = struct{}{}
	}
}

func TestAppErrorConstructionUnwrapIsAsAndStatusClassification(t *testing.T) {
	var nilAppErr *derr.AppError
	if nilAppErr.Error() != "" || nilAppErr.Unwrap() != nil {
		t.Fatalf("expected nil *AppError to return empty message and nil cause, got msg=%q cause=%v", nilAppErr.Error(), nilAppErr.Unwrap())
	}
	if _, ok := derr.AsAppError(nil); ok {
		t.Fatal("expected AsAppError(nil) to return false")
	}
	var typedNilErr error = nilAppErr
	if _, ok := derr.AsAppError(typedNilErr); ok {
		t.Fatal("expected AsAppError on typed nil *AppError to return false")
	}

	params := map[string]any{"expectedDigest": "abc"}
	digestErr := derr.New(derr.CodeDigestMismatch, 422, "cas digest mismatch", params)
	if digestErr.Code != derr.CodeDigestMismatch || digestErr.HTTPStatus != 422 || digestErr.Error() != "cas digest mismatch" || digestErr.Params["expectedDigest"] != "abc" {
		t.Fatalf("unexpected New AppError fields: %+v", digestErr)
	}
	if derr.StatusOf(digestErr) != 422 {
		t.Fatalf("expected StatusOf 422, got %d", derr.StatusOf(digestErr))
	}

	rootCause := stderrors.New("connection reset by peer")
	wrapped := derr.Wrap(derr.CodeCircuitOpen, 503, "", rootCause)
	if wrapped.Error() != "connection reset by peer" {
		t.Fatalf("expected Wrap with empty msg to surface root cause text, got %q", wrapped.Error())
	}
	if !stderrors.Is(wrapped, rootCause) {
		t.Fatal("expected errors.Is(wrapped, rootCause) to match via Unwrap")
	}

	outer := fmt.Errorf("tunnel layer: %w", derr.Wrap(derr.CodeUpstreamFailure, 502, "worker stream dropped", rootCause))
	if !stderrors.Is(outer, rootCause) {
		t.Fatal("expected transitive errors.Is through fmt.Errorf and AppError.Unwrap")
	}
	extracted, ok := derr.AsAppError(outer)
	if !ok || extracted.Code != derr.CodeUpstreamFailure || extracted.Error() != "worker stream dropped" {
		t.Fatalf("expected AsAppError to extract wrapped AppError, got ok=%v err=%+v", ok, extracted)
	}
	if derr.StatusOf(outer) != 502 {
		t.Fatalf("expected StatusOf(outer) == 502, got %d", derr.StatusOf(outer))
	}
}

func TestValidationfAndFallbackHTTPStatus(t *testing.T) {
	valErr := derr.Validationf(derr.CodeValidation, "field %s exceeds %d bytes", "name", 128)
	if valErr.Code != derr.CodeValidation || valErr.HTTPStatus != 400 || valErr.Error() != "field name exceeds 128 bytes" {
		t.Fatalf("unexpected Validationf output: %+v", valErr)
	}

	zeroStatusErr := &derr.AppError{Code: derr.CodeValidation, Message: "missing status"}
	if got := derr.StatusOf(zeroStatusErr); got != 400 {
		t.Fatalf("expected zero HTTPStatus AppError to default to 400, got %d", got)
	}
	if got := derr.StatusOf(stderrors.New("unclassified error")); got != 400 {
		t.Fatalf("expected plain error StatusOf to default to 400, got %d", got)
	}
	if got := derr.StatusOf(nil); got != 400 {
		t.Fatalf("expected nil error StatusOf to default to 400, got %d", got)
	}
}
