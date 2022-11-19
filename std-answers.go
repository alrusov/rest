package rest

import (
	"fmt"
	"net/http"
)

//----------------------------------------------------------------------------------------------------------------------------//

func NotImplemented(msg string, v ...any) (code int, err error) {
	return codeAndError(http.StatusNotImplemented, `not implemented`, msg, v...)
}

func NotAllowed(msg string, v ...any) (code int, err error) {
	return codeAndError(http.StatusMethodNotAllowed, "not allowed", msg, v...)
}

func BadRequest(msg string, v ...any) (code int, err error) {
	return codeAndError(http.StatusBadRequest, "bad request", msg, v...)
}

func NotFound(msg string, v ...any) (code int, err error) {
	return codeAndError(http.StatusNotFound, "not found", msg, v...)
}

func InternalServerError(msg string, v ...any) (code int, err error) {
	return codeAndError(http.StatusInternalServerError, "internal server error", msg, v...)
}

func codeAndError(code int, defaultMsg string, msg string, v ...any) (outCode int, err error) {
	if msg == "" {
		msg = defaultMsg
	}

	outCode = code
	err = fmt.Errorf(msg, v...)
	return
}

//----------------------------------------------------------------------------------------------------------------------------//
