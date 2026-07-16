package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/gin-gonic/gin"
)

func init() {
	router.NewGroupRouter("/api/v1/log").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/list", http.MethodGet).
				Handle(listLog),
		).
		AddRoute(
			router.NewRoute("/clear", http.MethodDelete).
				Handle(clearLog),
		).
		AddRoute(
			router.NewRoute("/stream-token", http.MethodGet).
				Handle(getStreamToken),
		)

	router.NewGroupRouter("/api/v1/log").
		AddRoute(
			router.NewRoute("/stream", http.MethodGet).
				Handle(streamLog),
		)
}

func listLog(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	startTimeStr := c.Query("start_time")
	endTimeStr := c.Query("end_time")

	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	if (startTimeStr == "") != (endTimeStr == "") {
		resp.Error(c, http.StatusBadRequest, "start_time and end_time must be supplied together")
		return
	}
	var startTime, endTime *int
	if startTimeStr != "" {
		st, err := strconv.Atoi(startTimeStr)
		if err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		et, err := strconv.Atoi(endTimeStr)
		if err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		startTime = &st
		endTime = &et
	}

	if cursorText, cursorMode := c.GetQuery("cursor"); cursorMode {
		beforeID, err := strconv.ParseInt(cursorText, 10, 64)
		if err != nil || beforeID < 0 {
			resp.Error(c, http.StatusBadRequest, "cursor must be a non-negative 64-bit integer")
			return
		}
		page, err := op.RelayLogListCursor(c.Request.Context(), startTime, endTime, beforeID, pageSize)
		if err != nil {
			respondInternalError(c, "list relay logs by cursor failed", err)
			return
		}
		items := make([]relayLogResponse, len(page.Items))
		for i := range page.Items {
			items[i] = relayLogResponse(page.Items[i])
		}
		resp.Success(c, relayLogCursorResponse{
			Items:      items,
			NextCursor: formatRelayLogCursor(page.NextCursor),
			HasMore:    page.HasMore,
		})
		return
	}

	logs, err := op.RelayLogList(c.Request.Context(), startTime, endTime, page, pageSize)
	if err != nil {
		respondInternalError(c, "list relay logs failed", err)
		return
	}

	resp.Success(c, logs)
}

type relayLogCursorResponse struct {
	Items      []relayLogResponse `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
	HasMore    bool               `json:"has_more"`
}

// relayLogResponse serializes the Snowflake primary key as a decimal string.
// JavaScript numbers cannot exactly represent current 64-bit Snowflake IDs.
type relayLogResponse model.RelayLog

func (entry relayLogResponse) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal(model.RelayLog(entry))
	if err != nil {
		return nil, err
	}
	fields := make(map[string]json.RawMessage)
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	id, err := json.Marshal(strconv.FormatInt(entry.ID, 10))
	if err != nil {
		return nil, err
	}
	fields["id"] = id
	return json.Marshal(fields)
}

func formatRelayLogCursor(cursor int64) string {
	if cursor <= 0 {
		return ""
	}
	return strconv.FormatInt(cursor, 10)
}

func clearLog(c *gin.Context) {
	if err := op.RelayLogClear(c.Request.Context()); err != nil {
		respondInternalError(c, "clear relay logs failed", err)
		return
	}
	resp.Success(c, nil)
}

func getStreamToken(c *gin.Context) {
	token, err := op.RelayLogStreamTokenCreate()
	if err != nil {
		if errors.Is(err, op.ErrRelayLogStreamTokenCapacity) {
			c.Header("Retry-After", "1")
			resp.Error(c, http.StatusTooManyRequests, err.Error())
			return
		}
		respondInternalError(c, "create relay log stream token failed", err)
		return
	}
	resp.Success(c, gin.H{"token": token})
}

func streamLog(c *gin.Context) {
	token := c.Query("token")
	afterID := int64(0)
	if afterText := c.Query("after"); afterText != "" {
		parsed, err := strconv.ParseInt(afterText, 10, 64)
		if err != nil || parsed < 0 {
			resp.Error(c, http.StatusBadRequest, "after must be a non-negative 64-bit integer")
			return
		}
		afterID = parsed
	}
	if token == "" || !op.RelayLogStreamTokenConsume(token) {
		resp.Error(c, http.StatusUnauthorized, "invalid stream token")
		return
	}

	logChan := op.RelayLogSubscribe()
	defer op.RelayLogUnsubscribe(logChan)

	ctx := c.Request.Context()
	replayed := make(map[int64]struct{})
	var missed []model.RelayLog
	truncated := false
	if afterID > 0 {
		var err error
		missed, truncated, err = op.RelayLogListAfter(ctx, afterID, 100)
		if err != nil {
			log.WithContext(ctx).Warnw("relay log replay query failed", "error", err)
			resp.Error(c, http.StatusInternalServerError, "failed to resume log stream")
			return
		}
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	if afterID > 0 {
		if truncated {
			gapData, _ := json.Marshal(gin.H{"after": strconv.FormatInt(afterID, 10)})
			if _, err := fmt.Fprintf(c.Writer, "event: gap\ndata: %s\n\n", gapData); err != nil {
				return
			}
		}
		for _, entry := range missed {
			if err := writeRelayLogSSE(c, entry); err != nil {
				log.WithContext(ctx).Warnw("relay log replay write failed", "error", err)
				return
			}
			replayed[entry.ID] = struct{}{}
		}
		c.Writer.Flush()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-logChan:
			if !ok {
				return
			}
			if _, duplicate := replayed[entry.ID]; duplicate {
				delete(replayed, entry.ID)
				continue
			}
			if err := writeRelayLogSSE(c, entry); err != nil {
				log.WithContext(ctx).Warnw("relay log stream write failed", "error", err)
				return
			}
			c.Writer.Flush()
		}
	}
}

func writeRelayLogSSE(c *gin.Context, entry model.RelayLog) error {
	data, err := json.Marshal(relayLogResponse(entry))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(c.Writer, "id: %d\ndata: %s\n\n", entry.ID, data)
	return err
}
