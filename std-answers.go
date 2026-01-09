package rest

import (
	"fmt"
	"net/http"
)

//----------------------------------------------------------------------------------------------------------------------------//

func BadRequest(msg string, v ...any) (code int, err error) {
	return makeError(http.StatusBadRequest, "bad request", msg, v...)
}

func Unauthorized(msg string, v ...any) (code int, err error) {
	return makeError(http.StatusUnauthorized, "unauthorized", msg, v...)
}

func Forbidden(msg string, v ...any) (code int, err error) {
	return makeError(http.StatusForbidden, "forbidden", msg, v...)
}

func NotFound(msg string, v ...any) (code int, err error) {
	return makeError(http.StatusNotFound, "not found", msg, v...)
}

func NotAllowed(msg string, v ...any) (code int, err error) {
	return makeError(http.StatusMethodNotAllowed, "method not allowed", msg, v...)
}

func Conflict(msg string, v ...any) (code int, err error) {
	return makeError(http.StatusConflict, "conflict", msg, v...)
}

func UnprocessableEntity(msg string, v ...any) (code int, err error) {
	return makeError(http.StatusUnprocessableEntity, "unprocessable entity", msg, v...)
}

func TooManyRequests(msg string, v ...any) (code int, err error) {
	return makeError(http.StatusTooManyRequests, "too many requests", msg, v...)
}

func InternalServerError(msg string, v ...any) (code int, err error) {
	return makeError(http.StatusInternalServerError, "internal server error", msg, v...)
}

func NotImplemented(msg string, v ...any) (code int, err error) {
	return makeError(http.StatusNotImplemented, "not implemented", msg, v...)
}

func ServiceUnavailable(msg string, v ...any) (code int, err error) {
	return makeError(http.StatusServiceUnavailable, "service unavailable", msg, v...)
}

//----------------------------------------------------------------------------------------------------------------------------//

// makeError creates formatted error with HTTP status code.
// If msg is empty, defaultMsg will be used.
func makeError(code int, defaultMsg, msg string, v ...any) (int, error) {
	if msg == "" {
		msg = defaultMsg
	}

	// Важно: не добавлять код статуса в текст ошибки
	// Клиенты API получат код отдельно в HTTP заголовке
	return code, fmt.Errorf(msg, v...)
}

//----------------------------------------------------------------------------------------------------------------------------//
