package handlers

import (
	"net/http"
	"strconv"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/routingstate"
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
	invalidateGroupRuntimeState(&group)
	resp.Success(c, group)
}

// resetGroupMemberCircuits 在分组被创建或更新（且处于启用态）后，清除其直连
// 渠道成员对应模型的熔断状态。用户手动启用/调整分组表达的是"立即投入使用"，
// 不应再受成员此前累积的熔断冷却压制；嵌套子分组在其自身被操作时同样生效。
func resetGroupMemberCircuits(group *model.Group) {
	if group == nil || !group.Enabled {
		return
	}
	for _, item := range group.Items {
		if item.Type == model.GroupItemTypeGroup || item.ChannelID <= 0 {
			continue
		}
		balancer.ResetCircuit(item.ChannelID, item.ModelName)
		relay.InvalidateChannelRuntimePenalties(item.ChannelID, item.ModelName)
	}
}

func invalidateGroupRuntimeState(group *model.Group) {
	balancer.InvalidateGroups()
	resetGroupMemberCircuits(group)
	routingstate.Notify()
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
	invalidateGroupRuntimeState(group)
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
	routingstate.Notify()
	resp.Success(c, "group deleted successfully")
}
