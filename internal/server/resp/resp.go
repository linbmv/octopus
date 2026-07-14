package resp

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type ResponseStruct struct {
	Code      int         `json:"code" example:"200"`
	Message   string      `json:"message" example:"success"`
	Data      interface{} `json:"data,omitempty"`
	Error     *ErrorInfo  `json:"error,omitempty"`
	RequestID string      `json:"request_id,omitempty"`
}

type ErrorInfo struct {
	Code    string                 `json:"code"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details,omitempty"`
}

func Success(c *gin.Context, data any) {
	requestID, _ := c.Get("request_id")
	response := ResponseStruct{
		Code:    http.StatusOK,
		Message: "success",
		Data:    data,
	}
	if requestID != nil {
		response.RequestID = requestID.(string)
	}
	c.JSON(http.StatusOK, response)
}

func Error(c *gin.Context, code int, err string) {
	ErrorWithDetails(c, code, err, nil)
}

func ErrorWithDetails(c *gin.Context, code int, message string, details map[string]interface{}) {
	requestID, _ := c.Get("request_id")
	response := ResponseStruct{
		Code:    code,
		Message: message,
		Error: &ErrorInfo{
			Code:    mapErrorCode(code),
			Message: message,
			Details: details,
		},
	}
	if requestID != nil {
		response.RequestID = requestID.(string)
	}
	c.AbortWithStatusJSON(code, response)
}

func mapErrorCode(httpCode int) string {
	switch httpCode {
	case http.StatusBadRequest:
		return "BAD_REQUEST"
	case http.StatusUnauthorized:
		return "UNAUTHORIZED"
	case http.StatusForbidden:
		return "FORBIDDEN"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusConflict:
		return "CONFLICT"
	case http.StatusTooManyRequests:
		return "RATE_LIMITED"
	case http.StatusInternalServerError:
		return "INTERNAL_ERROR"
	case http.StatusServiceUnavailable:
		return "SERVICE_UNAVAILABLE"
	default:
		return "UNKNOWN_ERROR"
	}
}
