package handlers

import (
	"errors"
	"net/http"

	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/gin-gonic/gin"
)

func respondOperationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, op.ErrInvalidInput):
		resp.Error(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, op.ErrNotFound):
		resp.Error(c, http.StatusNotFound, resp.ErrResourceNotFound)
	case errors.Is(err, op.ErrConflict):
		resp.Error(c, http.StatusConflict, err.Error())
	default:
		respondInternalError(c, "handler operation failed", err)
	}
}

func respondInternalError(c *gin.Context, operation string, err error) {
	log.WithContext(c.Request.Context()).Errorw(operation, "error", err)
	resp.Error(c, http.StatusInternalServerError, resp.ErrInternalServer)
}
