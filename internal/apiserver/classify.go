package apiserver

import (
	"errors"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Class is the structured class of an API server error.
// The class comes from the Status reason and details.causes[].reason.
type Class int

const (
	// ClassNone is a successful call.
	ClassNone Class = iota
	// ClassNoMatch means the mapper could not find the kind.
	ClassNoMatch
	// ClassImmutable is a field the server refuses to change.
	ClassImmutable
	// ClassUnknown is a strict-decoding failure. kube-apiserver reports that
	// as BadRequest with no field causes. Schema failures use Invalid and causes.
	ClassUnknown
	// ClassInvalid is a schema or CEL rejection that names a field cause.
	ClassInvalid
	// ClassUnclassified is not proof of a bad upgrade. Connection failures
	// and webhook errors land here and are not blockers.
	ClassUnclassified
)

// Classified is one server error reduced to the fields the report may use.
type Classified struct {
	Class  Class
	Field  string
	Fields []string
	Detail string
	Reason metav1.StatusReason
}

// Classify reads the Status reason and cause type.
// Two Kubernetes validation details have no cause type of their own:
// an immutable field ends with "field is immutable", and an apply of an
// unknown built-in field ends with "field not declared in schema".
// The quoted value in an immutable message is not kept.
func Classify(err error) Classified {
	if err == nil {
		return Classified{Class: ClassNone}
	}
	if meta.IsNoMatchError(err) {
		return Classified{Class: ClassNoMatch}
	}
	var statusErr *apierrors.StatusError
	if !errors.As(err, &statusErr) {
		var api apierrors.APIStatus
		if errors.As(err, &api) {
			statusErr = &apierrors.StatusError{ErrStatus: api.Status()}
		} else {
			if field, detail, ok := typedMismatch(err.Error()); ok {
				return Classified{Class: ClassInvalid, Field: field, Detail: detail}
			}
			return Classified{Class: ClassUnclassified}
		}
	}
	st := statusErr.ErrStatus
	out := Classified{Reason: st.Reason}
	var causes []metav1.StatusCause
	if st.Details != nil {
		causes = append([]metav1.StatusCause(nil), st.Details.Causes...)
		// Sort causes so repeated runs report the same field first regardless of
		// kube-apiserver's internal map iteration order.
		sort.Slice(causes, func(i, j int) bool {
			if causes[i].Field != causes[j].Field {
				return causes[i].Field < causes[j].Field
			}
			if causes[i].Type != causes[j].Type {
				return causes[i].Type < causes[j].Type
			}
			return causes[i].Message < causes[j].Message
		})
	}
	var invalidFields []string
	seenField := map[string]bool{}
	var firstInvalid *Classified
	for _, cause := range causes {
		switch cause.Type {
		case metav1.CauseTypeForbidden:
			return Classified{Class: ClassImmutable, Field: cause.Field, Reason: st.Reason}
		case metav1.CauseTypeFieldValueInvalid:
			// An immutable update is FieldValueInvalid. The validation detail is
			// the suffix "field is immutable". The message also quotes the
			// rejected value, so that text is not kept.
			if strings.HasSuffix(cause.Message, "field is immutable") {
				return Classified{Class: ClassImmutable, Field: cause.Field, Reason: st.Reason}
			}
			if cause.Field != "" && !seenField[cause.Field] {
				seenField[cause.Field] = true
				invalidFields = append(invalidFields, cause.Field)
			}
			if firstInvalid == nil {
				c := Classified{Class: ClassInvalid, Field: cause.Field, Detail: scrubDetail(cause.Message), Reason: st.Reason}
				firstInvalid = &c
			}
		case metav1.CauseTypeFieldValueNotSupported,
			metav1.CauseTypeFieldValueRequired,
			metav1.CauseTypeTypeInvalid,
			metav1.CauseTypeFieldValueNotFound,
			metav1.CauseTypeFieldValueDuplicate,
			metav1.CauseTypeTooLong,
			metav1.CauseTypeTooMany:
			// An unsupported enum value is a rejection, not an unknown field.
			if cause.Field != "" && !seenField[cause.Field] {
				seenField[cause.Field] = true
				invalidFields = append(invalidFields, cause.Field)
			}
			if firstInvalid == nil {
				c := Classified{Class: ClassInvalid, Field: cause.Field, Detail: scrubDetail(cause.Message), Reason: st.Reason}
				firstInvalid = &c
			}
		}
	}
	if firstInvalid != nil {
		firstInvalid.Fields = invalidFields
		return *firstInvalid
	}
	if field, ok := typedUnknownField(st); ok {
		return Classified{Class: ClassUnknown, Field: field, Reason: st.Reason}
	}
	if field, detail, ok := typedMismatch(st.Message); ok {
		return Classified{Class: ClassInvalid, Field: field, Fields: []string{field}, Detail: detail, Reason: st.Reason}
	}
	switch st.Reason {
	case metav1.StatusReasonBadRequest:
		// Strict decoding of an unknown field is a BadRequest with no causes.
		// Schema validation is Invalid and carries causes, handled above.
		if len(causes) == 0 {
			return Classified{Class: ClassUnknown, Reason: st.Reason}
		}
		return Classified{Class: ClassInvalid, Reason: st.Reason}
	case metav1.StatusReasonInvalid:
		return Classified{Class: ClassInvalid, Detail: firstCause(causes), Reason: st.Reason}
	case metav1.StatusReasonNotFound:
		// A missing object is not a removed API. A removed API fails mapping
		// before the request, which is ClassNoMatch.
		return Classified{Class: ClassUnclassified, Reason: st.Reason}
	default:
		out.Class = ClassUnclassified
		return out
	}
}

// typedUnknownField recognises the apply error kube-apiserver returns when a
// built-in object has a field the schema does not declare. The Status has no
// causes. The field path is the last segment of that error.
func typedUnknownField(st metav1.Status) (string, bool) {
	const marker = ": field not declared in schema"
	if !strings.HasSuffix(st.Message, marker) {
		return "", false
	}
	head := strings.TrimSuffix(st.Message, marker)
	i := strings.LastIndex(head, ": ")
	if i < 0 {
		return "", true
	}
	return strings.TrimPrefix(head[i+2:], "."), true
}

// scrubDetail drops the rejected value from a Kubernetes validation message.
// "Invalid value: \"secret\": expected integer" becomes "expected integer".
// The constraint that follows, including supported enum values, stays.
func scrubDetail(msg string) string {
	for _, prefix := range []string{"Invalid value: ", "Unsupported value: "} {
		msg = stripPrefixedValue(msg, prefix)
	}
	return strings.TrimSpace(scrubGotQuoted(msg))
}

func stripPrefixedValue(msg, prefix string) string {
	for {
		i := strings.Index(msg, prefix)
		if i < 0 {
			return msg
		}
		rest := msg[i+len(prefix):]
		n, ok := consumeValue(rest)
		if !ok {
			return msg
		}
		tail := rest[n:]
		switch {
		case strings.HasPrefix(tail, ": "):
			tail = tail[2:]
		case strings.HasPrefix(tail, ":"):
			tail = strings.TrimPrefix(tail[1:], " ")
		}
		msg = msg[:i] + tail
	}
}

func consumeValue(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	if s[0] == '"' {
		return consumeQuoted(s)
	}
	if i := strings.Index(s, ": "); i >= 0 {
		return i, true
	}
	// An unquoted value with no ": " still has to leave the message.
	// Otherwise the rejected value is printed.
	return len(s), true
}

func consumeQuoted(s string) (int, bool) {
	if len(s) < 2 || s[0] != '"' {
		return 0, false
	}
	escaped := false
	for i := 1; i < len(s); i++ {
		if escaped {
			escaped = false
			continue
		}
		if s[i] == '\\' {
			escaped = true
			continue
		}
		if s[i] == '"' {
			return i + 1, true
		}
	}
	return 0, false
}

func scrubGotQuoted(msg string) string {
	const marker = "got \""
	i := strings.Index(msg, marker)
	if i < 0 {
		return msg
	}
	rest := msg[i+len("got "):]
	n, ok := consumeQuoted(rest)
	if !ok {
		return msg
	}
	return msg[:i] + "got string" + rest[n:]
}

// typedMismatch recognises a server-side apply error for a value of the wrong
// type. kube-apiserver often returns that as a typed-patch message with no
// field cause. An unknown field keeps its own class.
func typedMismatch(msg string) (string, string, bool) {
	if msg == "" || strings.Contains(msg, "field not declared in schema") {
		return "", "", false
	}
	if !strings.Contains(msg, "expected ") {
		return "", "", false
	}
	if !strings.Contains(msg, "got ") && !strings.Contains(msg, "must be of type") {
		return "", "", false
	}
	const marker = ": expected "
	i := strings.Index(msg, marker)
	if i < 0 {
		return "", scrubDetail(msg), true
	}
	head := msg[:i]
	field := head
	if j := strings.LastIndex(head, " "); j >= 0 {
		field = head[j+1:]
	}
	field = strings.TrimPrefix(field, ".")
	return field, scrubDetail(msg[i+2:]), true
}

func firstCause(causes []metav1.StatusCause) string {
	if len(causes) == 0 {
		return ""
	}
	if causes[0].Field != "" {
		return causes[0].Field
	}
	return ""
}

// NoMatch is a mapper error for tests and for group checks.
func noMatch(group, version, resource string) error {
	return &meta.NoResourceMatchError{PartialResource: schema.GroupVersionResource{
		Group: group, Version: version, Resource: resource,
	}}
}
