// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"errors"
	"strings"
)

var errContentPolicyRejection = errors.New("llm content policy rejection")

type contentPolicyRejectionError struct {
	cause error
}

func (e contentPolicyRejectionError) Error() string {
	if e.cause == nil {
		return errContentPolicyRejection.Error()
	}
	return e.cause.Error()
}

func (e contentPolicyRejectionError) Unwrap() error {
	return e.cause
}

func (e contentPolicyRejectionError) Is(target error) bool {
	return target == errContentPolicyRejection
}

func classifyLLMError(err error) error {
	if err == nil || errors.Is(err, errContentPolicyRejection) || !isContentPolicyRejection(err) {
		return err
	}
	return contentPolicyRejectionError{cause: err}
}

func isContentPolicyRejection(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"content_policy_violation",
		"content policy violation",
		"content filter",
		"content_filter",
		"safety policy",
		"request was rejected because it was considered high risk",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return strings.Contains(text, "high risk") &&
		(strings.Contains(text, "reject") || strings.Contains(text, "refus"))
}

func isModelUnavailableLLMError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"model_not_found",
		"unsupported_model",
		"model not found",
		"model is not available",
		"model is unavailable",
		"does not support model",
		"doesn't support model",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return strings.Contains(text, "model") &&
		strings.Contains(text, "not supported by any configured account")
}
