package apiserver

import (
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestClassifyUsesStatusFields(t *testing.T) {
	immutable := &apierrors.StatusError{ErrStatus: metav1.Status{
		Reason: metav1.StatusReasonInvalid,
		Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{
			Type:  metav1.CauseTypeForbidden,
			Field: "spec.template",
		}}},
	}}
	if got := Classify(immutable); got.Class != ClassImmutable || got.Field != "spec.template" {
		t.Fatalf("immutable: %+v", got)
	}

	invalid := &apierrors.StatusError{ErrStatus: metav1.Status{
		Reason: metav1.StatusReasonInvalid,
		Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{
			Type:    metav1.CauseTypeFieldValueRequired,
			Field:   "spec.schedule",
			Message: "Required value",
		}}},
	}}
	if got := Classify(invalid); got.Class != ClassInvalid || got.Field != "spec.schedule" || got.Detail != "Required value" {
		t.Fatalf("invalid: %+v", got)
	}

	unknown := &apierrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonBadRequest}}
	if got := Classify(unknown); got.Class != ClassUnknown {
		t.Fatalf("unknown: %+v", got)
	}
	typed := &apierrors.StatusError{ErrStatus: metav1.Status{
		Code:    500,
		Message: `failed to create typed patch object (/app; apps/v1, Kind=Deployment): .spec.template.spec.containers[name="app"].notAField: field not declared in schema`,
	}}
	if got := Classify(typed); got.Class != ClassUnknown || got.Field != `spec.template.spec.containers[name="app"].notAField` {
		t.Fatalf("typed unknown: %+v", got)
	}
	immutableMsg := &apierrors.StatusError{ErrStatus: metav1.Status{
		Reason: metav1.StatusReasonInvalid,
		Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{
			Type:    metav1.CauseTypeFieldValueInvalid,
			Field:   "spec.template",
			Message: `Invalid value: {"image":"secret-value"}: field is immutable`,
		}}},
	}}
	if got := Classify(immutableMsg); got.Class != ClassImmutable || got.Field != "spec.template" || got.Detail != "" {
		t.Fatalf("immutable detail must not keep the value: %+v", got)
	}

	if got := Classify(noMatch("batch", "v1beta1", "cronjobs")); got.Class != ClassNoMatch {
		t.Fatalf("no match: %+v", got)
	}
	if got := Classify(nil); got.Class != ClassNone {
		t.Fatalf("nil: %+v", got)
	}
}

func TestClassifyInvalidEnumIsInvalid(t *testing.T) {
	err := &apierrors.StatusError{ErrStatus: metav1.Status{
		Reason: metav1.StatusReasonInvalid,
		Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{
			Type:    metav1.CauseTypeFieldValueNotSupported,
			Field:   "spec.restartPolicy",
			Message: `Unsupported value: "Bogus": supported values: "Always", "OnFailure", "Never"`,
		}}},
	}}
	got := Classify(err)
	if got.Class != ClassInvalid || got.Field != "spec.restartPolicy" {
		t.Fatalf("enum: %+v", got)
	}
	if strings.Contains(got.Detail, "Bogus") {
		t.Fatalf("rejected enum value leaked: %q", got.Detail)
	}
	if !strings.Contains(got.Detail, "supported values") {
		t.Fatalf("constraint dropped: %q", got.Detail)
	}
}

func TestClassifyScrubsRejectedValues(t *testing.T) {
	const secret = "super-secret-token"
	invalid := &apierrors.StatusError{ErrStatus: metav1.Status{
		Reason: metav1.StatusReasonInvalid,
		Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{
			Type:    metav1.CauseTypeFieldValueInvalid,
			Field:   "spec.size",
			Message: `Invalid value: "super-secret-token": spec.size in body must be of type integer: "string"`,
		}}},
	}}
	got := Classify(invalid)
	if got.Class != ClassInvalid || strings.Contains(got.Detail, secret) {
		t.Fatalf("field value: %+v", got)
	}
	if !strings.Contains(got.Detail, "must be of type integer") {
		t.Fatalf("reason dropped: %q", got.Detail)
	}

	typed := &apierrors.StatusError{ErrStatus: metav1.Status{
		Reason: metav1.StatusReasonInvalid,
		Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{
			Type:    metav1.CauseTypeTypeInvalid,
			Field:   "spec.size",
			Message: `Invalid value: "super-secret-token": expected integer`,
		}}},
	}}
	got = Classify(typed)
	if got.Class != ClassInvalid || strings.Contains(got.Detail, secret) || got.Detail != "expected integer" {
		t.Fatalf("type invalid: %+v", got)
	}
}

func TestClassifyUnquotedInvalidValueIsScrubbed(t *testing.T) {
	const secret = "super-secret-token"
	err := &apierrors.StatusError{ErrStatus: metav1.Status{
		Reason: metav1.StatusReasonInvalid,
		Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{
			Type:    metav1.CauseTypeFieldValueInvalid,
			Field:   "data.token",
			Message: "Invalid value: " + secret,
		}}},
	}}
	got := Classify(err)
	if got.Class != ClassInvalid || strings.Contains(got.Detail, secret) {
		t.Fatalf("unquoted value leaked: %+v", got)
	}
}

func TestClassifyTypedPatchTypeMismatch(t *testing.T) {
	const secret = "super-secret-token"
	err := &apierrors.StatusError{ErrStatus: metav1.Status{
		Code:    500,
		Message: `failed to create typed patch object (widgets.example.com/v1, Kind=Widget): .spec.size: expected integer, got "super-secret-token"`,
	}}
	got := Classify(err)
	if got.Class != ClassInvalid || got.Field != "spec.size" {
		t.Fatalf("typed mismatch: %+v", got)
	}
	if strings.Contains(got.Detail, secret) {
		t.Fatalf("typed patch leaked the value: %q", got.Detail)
	}
	if !strings.Contains(got.Detail, "expected integer") {
		t.Fatalf("typed patch detail: %q", got.Detail)
	}
}
