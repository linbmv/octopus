package resp

const (
	ErrBadRequest        = "Invalid request parameters"
	ErrInvalidJSON       = "Invalid JSON format"
	ErrInvalidParam      = "Invalid parameter"
	ErrValidation        = "Input validation failed"
	ErrDuplicateResource = "Resource already exists"
	ErrResourceNotFound  = "Resource not found"
	ErrInternalServer    = "An unexpected error occurred"
	ErrDatabase          = "Database operation failed"
	ErrUnauthorized      = "Authentication failed"
	ErrCSRFValidation    = "CSRF validation failed"
	ErrPasswordChange    = "Password change required before using this resource"
	ErrPasswordPolicy    = "Password must contain between 8 and 72 UTF-8 bytes"
	ErrPasswordUnchanged = "New password must differ from current password"
	ErrUsernameUnchanged = "New username must differ from current username"

	CodePasswordChangeRequired = "PASSWORD_CHANGE_REQUIRED"
	CodeCSRFValidationFailed   = "CSRF_VALIDATION_FAILED"
)
