package dynamodb

import (
	"fmt"
	"net/http"
)

// apiError is a DynamoDB error. It is converted to an awsapi.Error for the AWS
// protocol (keeping Fields such as CancellationReasons) and to a core.Error for
// the native API.
type apiError struct {
	Status  int
	Code    string
	Message string
	Fields  map[string]any
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func errf(code, format string, a ...any) *apiError {
	return &apiError{Status: http.StatusBadRequest, Code: code, Message: fmt.Sprintf(format, a...)}
}

func validation(format string, a ...any) *apiError {
	return errf("ValidationException", format, a...)
}

func invalidParam(format string, a ...any) *apiError {
	return validation("One or more parameter values were invalid: "+format, a...)
}

func tableNotFound(name string) *apiError {
	return errf("ResourceNotFoundException", "Requested resource not found: Table: %s not found", name)
}

var errNotFoundGeneric = errf("ResourceNotFoundException", "Requested resource not found")

func conditionFailed(old Item, returnOld bool) *apiError {
	e := errf("ConditionalCheckFailedException", "The conditional request failed")
	if returnOld && old != nil {
		e.Fields = map[string]any{"Item": old}
	}
	return e
}
