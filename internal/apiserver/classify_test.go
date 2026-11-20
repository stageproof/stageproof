package apiserver

import (
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
