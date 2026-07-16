package handlers

import (
	"net/http"
	"strconv"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/gin-gonic/gin"
)

func init() {
	router.NewGroupRouter("/api/v1/group").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/list", http.MethodGet).
				Handle(getGroupList),
		).
		AddRoute(
			router.NewRoute("/create", http.MethodPost).
				Handle(createGroup),
		).
		AddRoute(
			router.NewRoute("/update", http.MethodPost).
				Handle(updateGroup),
		).
		AddRoute(
			router.NewRoute("/delete/:id", http.MethodDelete).
				Handle(deleteGroup),
		)
}

func getGroupList(c *gin.Context) {
	groups, err := op.GroupList(c.Request.Context())
	if err != nil {
		respondInternalError(c, "list groups failed", err)
		return
	}
	resp.Success(c, groups)
}

type groupCreateRequest struct {
	Name              string                      `json:"name"`
	Mode              model.GroupMode             `json:"mode"`
	MatchRegex        string                      `json:"match_regex,omitempty"`
	FirstTokenTimeOut int                         `json:"first_token_time_out,omitempty"`
	SessionKeepTime   int                         `json:"session_keep_time,omitempty"`
	Items             []model.GroupItemAddRequest `json:"items,omitempty"`
}

func (r groupCreateRequest) group() model.Group {
	items := make([]model.GroupItem, len(r.Items))
	for i, item := range r.Items {
		items[i] = model.GroupItem{
			Type:          item.Type,
			ChannelID:     item.ChannelID,
			TargetGroupID: item.TargetGroupID,
			ModelName:     item.ModelName,
			Priority:      item.Priority,
			Weight:        item.Weight,
		}
	}
	return model.Group{
		Name:              r.Name,
		Mode:              r.Mode,
		MatchRegex:        r.MatchRegex,
		FirstTokenTimeOut: r.FirstTokenTimeOut,
		SessionKeepTime:   r.SessionKeepTime,
		Items:             items,
	}
}

func createGroup(c *gin.Context) {
	var request groupCreateRequest
	if err := bindStrictJSON(c, &request); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	group := request.group()
	if err := model.ValidateGroup(&group); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if group.MatchRegex != "" {
		if err := validateModelMatchRegex(group.MatchRegex); err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := op.GroupCreate(&group, c.Request.Context()); err != nil {
		respondOperationError(c, err)
		return
	}
	balancer.InvalidateGroups()
	resp.Success(c, group)
}

func updateGroup(c *gin.Context) {
	var req model.GroupUpdateRequest
	if err := bindStrictJSON(c, &req); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := model.ValidateGroupUpdate(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if req.MatchRegex != nil {
		if err := validateModelMatchRegex(*req.MatchRegex); err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
	}
	group, err := op.GroupUpdate(&req, c.Request.Context())
	if err != nil {
		respondOperationError(c, err)
		return
	}
	balancer.InvalidateGroups()
	resp.Success(c, group)
}

func deleteGroup(c *gin.Context) {
	id := c.Param("id")
	idNum, err := strconv.Atoi(id)
	if err != nil || idNum <= 0 {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	if err := op.GroupDel(idNum, c.Request.Context()); err != nil {
		respondOperationError(c, err)
		return
	}
	balancer.InvalidateGroups()
	resp.Success(c, "group deleted successfully")
}
