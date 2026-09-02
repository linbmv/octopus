package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/bestruirui/octopus/internal/task"
	"github.com/gin-gonic/gin"
)

func init() {
	router.NewGroupRouter("/api/v1/setting").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/list", http.MethodGet).
				Handle(getSettingList),
		).
		AddRoute(
			router.NewRoute("/set", http.MethodPost).
				Use(middleware.RequireJSON()).
				Handle(setSetting),
		).
		AddRoute(
			router.NewRoute("/export", http.MethodGet).
				Handle(exportDB),
		).
		AddRoute(
			router.NewRoute("/export-config", http.MethodGet).
				Handle(exportConfig),
		).
		AddRoute(
			router.NewRoute("/import", http.MethodPost).
				Handle(importDB),
		).
		AddRoute(
			router.NewRoute("/import-config", http.MethodPost).
				Handle(importConfig),
		)
}

func getSettingList(c *gin.Context) {
	settings, err := op.SettingList(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, settings)
}

func setSetting(c *gin.Context) {
	var setting model.Setting
	if err := c.ShouldBindJSON(&setting); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := setting.Validate(); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := op.SettingSetString(setting.Key, setting.Value); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	switch setting.Key {
	case model.SettingKeyModelInfoUpdateInterval:
		hours, err := strconv.Atoi(setting.Value)
		if err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		task.Update(string(setting.Key), time.Duration(hours)*time.Hour)
	case model.SettingKeySyncLLMInterval:
		hours, err := strconv.Atoi(setting.Value)
		if err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		task.Update(string(setting.Key), time.Duration(hours)*time.Hour)
	}
	resp.Success(c, setting)
}

func exportDB(c *gin.Context) {
	dump, err := op.DBExportAll(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.Header("Content-Type", "application/json")
	c.Header("Content-Disposition", "attachment; filename=\"octopus-export-"+time.Now().Format("20060102150405")+".json\"")
	c.JSON(http.StatusOK, dump)
}

func exportConfig(c *gin.Context) {
	password := []byte(c.GetHeader("X-Octopus-Backup-Password"))
	c.Request.Header.Del("X-Octopus-Backup-Password")
	defer clear(password)
	encrypted, err := op.DBExportConfigEncrypted(c.Request.Context(), password)
	if err != nil {
		if errors.Is(err, op.ErrDBBackupPasswordRequired) || errors.Is(err, op.ErrDBBackupPasswordInvalid) {
			resp.Error(c, http.StatusBadRequest, err.Error())
		} else {
			resp.Error(c, http.StatusInternalServerError, "config backup export failed")
		}
		return
	}

	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", op.EncryptedDBBackupContentType)
	c.Header("Content-Disposition", "attachment; filename=\"octopus-config-"+time.Now().Format("20060102150405")+op.EncryptedDBBackupExtension+"\"")
	c.Data(http.StatusOK, op.EncryptedDBBackupContentType, encrypted)
	clear(encrypted)
}

func importDB(c *gin.Context) {
	var dump model.DBDump

	contentType := c.GetHeader("Content-Type")
	if strings.Contains(contentType, "multipart/form-data") {
		fh, err := c.FormFile("file")
		if err != nil {
			resp.Error(c, http.StatusBadRequest, "missing upload file field 'file'")
			return
		}
		f, err := fh.Open()
		if err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		defer f.Close()
		body, err := io.ReadAll(f)
		if err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if err := decodeDBDump(body, &dump); err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if err := decodeDBDump(body, &dump); err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
	}

	seenLLMNames := make(map[string]struct{}, len(dump.LLMInfos))
	for i := range dump.LLMInfos {
		dump.LLMInfos[i].Name = strings.ToLower(strings.TrimSpace(dump.LLMInfos[i].Name))
		if dump.LLMInfos[i].Name == "" {
			resp.Error(c, http.StatusBadRequest, "model price name cannot be empty")
			return
		}
		if _, ok := seenLLMNames[dump.LLMInfos[i].Name]; ok {
			resp.Error(c, http.StatusBadRequest, "duplicate model price: "+dump.LLMInfos[i].Name)
			return
		}
		seenLLMNames[dump.LLMInfos[i].Name] = struct{}{}
	}
	for i := range dump.Groups {
		if dump.Groups[i].Mode == "" {
			dump.Groups[i].Mode = model.GroupModeManual
		}
		model.NormalizeGroupRelayConfig(&dump.Groups[i].RelayConfig)
		if !isSupportedGroupMode(dump.Groups[i].Mode) {
			resp.Error(c, http.StatusBadRequest, "invalid group relay mode")
			return
		}
	}

	result, err := op.DBImportIncremental(c.Request.Context(), &dump)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}

	if err := op.InitCache(); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	resp.Success(c, result)
}

func importConfig(c *gin.Context) {
	password := []byte(c.GetHeader("X-Octopus-Backup-Password"))
	c.Request.Header.Del("X-Octopus-Backup-Password")
	defer clear(password)
	body, err := readImportBody(c)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}

	dump, err := op.DecodeConfigDump(body, password)
	clear(body)
	if err != nil {
		if errors.Is(err, op.ErrDBBackupPasswordRequired) || errors.Is(err, op.ErrDBBackupPasswordInvalid) || errors.Is(err, op.ErrDBBackupAuthentication) || errors.Is(err, op.ErrDBBackupUnsupported) || errors.Is(err, op.ErrDBBackupInvalidEnvelope) {
			resp.Error(c, http.StatusBadRequest, "invalid or undecryptable configuration backup")
		} else {
			resp.Error(c, http.StatusBadRequest, err.Error())
		}
		return
	}
	for i := range dump.Groups {
		if dump.Groups[i].Mode == "" {
			dump.Groups[i].Mode = model.GroupModeManual
		}
		model.NormalizeGroupRelayConfig(&dump.Groups[i].RelayConfig)
		if !isSupportedGroupMode(dump.Groups[i].Mode) {
			resp.Error(c, http.StatusBadRequest, "invalid group relay mode")
			return
		}
	}

	result, err := op.DBImportConfig(c.Request.Context(), dump)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := op.InitCache(); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, result)
}

func readImportBody(c *gin.Context) ([]byte, error) {
	maxBytes := int64(op.ConfigBackupMaxBytes) + op.DBBackupEnvelopeOverhead
	contentType := c.GetHeader("Content-Type")
	if strings.Contains(contentType, "multipart/form-data") {
		fh, err := c.FormFile("file")
		if err != nil {
			return nil, fmt.Errorf("missing upload file field 'file'")
		}
		f, err := fh.Open()
		if err != nil {
			return nil, err
		}
		defer f.Close()
		body, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
		if err != nil {
			return nil, err
		}
		if int64(len(body)) > maxBytes {
			return nil, op.ErrDBBackupTooLarge
		}
		return body, nil
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, op.ErrDBBackupTooLarge
	}
	return body, nil
}

func decodeDBDump(body []byte, dump *model.DBDump) error {
	if dump == nil {
		return json.Unmarshal(body, &struct{}{})
	}

	if err := json.Unmarshal(body, dump); err != nil {
		return err
	}

	if dump.Version == 0 &&
		len(dump.Channels) == 0 &&
		len(dump.ChannelKeys) == 0 &&
		len(dump.Groups) == 0 &&
		len(dump.ChannelModels) == 0 &&
		len(dump.GroupItems) == 0 &&
		len(dump.Settings) == 0 &&
		len(dump.APIKeys) == 0 &&
		len(dump.LLMInfos) == 0 &&
		len(dump.StatsDaily) == 0 &&
		len(dump.StatsHourly) == 0 &&
		len(dump.StatsTotal) == 0 &&
		len(dump.StatsAPIKey) == 0 {
		var wrapper struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &wrapper); err == nil && len(wrapper.Data) > 0 {
			return json.Unmarshal(wrapper.Data, dump)
		}
	}

	return nil
}

func isSupportedGroupMode(mode model.GroupMode) bool {
	switch mode {
	case model.GroupModeManual, model.GroupModeFailover, model.GroupModeRoundRobin, model.GroupModeRandom, model.GroupModeWeighted:
		return true
	default:
		return false
	}
}
