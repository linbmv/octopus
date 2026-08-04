package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/channelstate"
	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/bestruirui/octopus/internal/task"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/gin-gonic/gin"
)

var (
	refreshCachesAfterDBImport = op.InitCache
	dbExportByteLimit          = int64(conf.MaxDBExportBytes)
)

const (
	backupPasswordHeader         = "X-Octopus-Backup-Password"
	maxDBImportMultipartMemory   = 1 << 20
	maxDBImportMultipartOverhead = 1 << 20
)

var (
	errBackupPasswordInURL      = errors.New("database backup password must not be supplied in the URL")
	errBackupPasswordDuplicated = errors.New("database backup password must be supplied only once")
	errBackupUploadMissing      = errors.New("missing upload file field 'file'")
	errBackupUploadDuplicated   = errors.New("database backup upload must contain exactly one file")
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
			router.NewRoute("/import", http.MethodPost).
				Handle(importDB),
		)
}

func getSettingList(c *gin.Context) {
	settings, err := op.SettingList(c.Request.Context())
	if err != nil {
		respondInternalError(c, "list settings failed", err)
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
	if err := op.SettingSetStringContext(c.Request.Context(), setting.Key, setting.Value); err != nil {
		respondInternalError(c, "update setting failed", err)
		return
	}
	middleware.AuditLog(c, middleware.EventSettingsUpdate, map[string]interface{}{
		"key":     setting.Key,
		"changed": true,
	})
	if err := task.ReconfigureSetting(setting.Key, setting.Value); err != nil {
		respondInternalError(c, "reconfigure setting task failed", err)
		return
	}
	relay.ReloadHealthSettings()
	resp.Success(c, setting)
}

func exportDB(c *gin.Context) {
	if c.Request.URL.Query().Has("password") {
		resp.Error(c, http.StatusBadRequest, errBackupPasswordInURL.Error())
		return
	}
	includeLogs, _ := strconv.ParseBool(c.DefaultQuery("include_logs", "false"))
	includeStats, _ := strconv.ParseBool(c.DefaultQuery("include_stats", "false"))
	password := []byte(c.GetHeader(backupPasswordHeader))
	c.Request.Header.Del(backupPasswordHeader)
	defer clear(password)

	c.Header("Cache-Control", "no-store")
	filename := "octopus-export-" + time.Now().Format("20060102150405")
	if len(password) != 0 {
		encrypted, err := op.DBExportAllEncryptedBounded(c.Request.Context(), password, includeLogs, includeStats, dbExportByteLimit)
		if err != nil {
			respondDBBackupInputError(c, err)
			return
		}
		defer clear(encrypted)
		c.Header("Content-Disposition", "attachment; filename=\""+filename+op.EncryptedDBBackupExtension+"\"")
		c.Data(http.StatusOK, op.EncryptedDBBackupContentType, encrypted)
		return
	}

	spool, size, err := spoolPlainDBExport(c.Request.Context(), includeLogs, includeStats, dbExportByteLimit)
	if err != nil {
		if errors.Is(err, op.ErrDBBackupTooLarge) {
			respondDBBackupInputError(c, err)
			return
		}
		log.WithContext(c.Request.Context()).Errorw("database export failed before response", "error", err)
		resp.ErrorWithCode(c, http.StatusInternalServerError, "DB_EXPORT_FAILED", "database export failed")
		return
	}
	defer func() {
		if err := spool.Close(); err != nil {
			log.WithContext(c.Request.Context()).Warnw("failed to close database export spool", "error", err)
		}
	}()

	c.Header("Content-Type", "application/json")
	c.Header("Content-Disposition", "attachment; filename=\""+filename+".json\"")
	c.Header("Content-Length", strconv.FormatInt(size, 10))
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	written, err := io.CopyN(c.Writer, spool.file, size)
	if err != nil {
		// Status and headers are already on the wire. Content-Length makes a short
		// body detectable by HTTP clients even though the status cannot be changed.
		log.WithContext(c.Request.Context()).Errorw("database export response write failed", "error", err, "written_bytes", written, "expected_bytes", size)
	}
}

type dbExportSpool struct {
	file *os.File
	path string
}

func spoolPlainDBExport(ctx context.Context, includeLogs, includeStats bool, maxBytes int64) (*dbExportSpool, int64, error) {
	file, err := os.CreateTemp("", ".octopus-export-*.json")
	if err != nil {
		return nil, 0, fmt.Errorf("create database export spool: %w", err)
	}
	spool := &dbExportSpool{file: file, path: file.Name()}
	// Unix permits unlinking an open file, eliminating a directory entry even if
	// the process exits unexpectedly. Windows keeps the 0600 CreateTemp file and
	// removes it when Close runs.
	if err := os.Remove(spool.path); err == nil {
		spool.path = ""
	}
	fail := func(exportErr error) (*dbExportSpool, int64, error) {
		return nil, 0, errors.Join(exportErr, spool.Close())
	}
	if err := file.Chmod(0o600); err != nil {
		return fail(fmt.Errorf("secure database export spool: %w", err))
	}
	if err := op.DBExportAllStreamBounded(ctx, file, includeLogs, includeStats, maxBytes); err != nil {
		return fail(err)
	}
	info, err := file.Stat()
	if err != nil {
		return fail(fmt.Errorf("inspect database export spool: %w", err))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(fmt.Errorf("rewind database export spool: %w", err))
	}
	return spool, info.Size(), nil
}

func (s *dbExportSpool) Close() error {
	if s == nil || s.file == nil {
		return nil
	}
	closeErr := s.file.Close()
	s.file = nil
	var removeErr error
	if s.path != "" {
		removeErr = os.Remove(s.path)
		s.path = ""
	}
	return errors.Join(closeErr, removeErr)
}

func importDB(c *gin.Context) {
	if c.Request.URL.Query().Has("password") {
		resp.Error(c, http.StatusBadRequest, errBackupPasswordInURL.Error())
		return
	}
	options, err := parseDBImportOptions(c)
	if err != nil {
		resp.ErrorWithCode(c, http.StatusBadRequest, "DB_IMPORT_OPTIONS_INVALID", err.Error())
		return
	}
	body, password, err := readDBImportRequest(c)
	if err != nil {
		respondDBBackupInputError(c, err)
		return
	}
	defer clear(body)
	defer clear(password)
	if len(password) != 0 {
		if err := op.ValidateDBBackupPassword(password); err != nil {
			respondDBBackupInputError(c, err)
			return
		}
	}

	plaintext, err := op.DecodeDBBackup(body, password, conf.MaxDBImportBytes)
	if err != nil {
		respondDBBackupInputError(c, err)
		return
	}
	defer clear(plaintext)
	var dump model.DBDump
	if err := decodeDBDump(plaintext, &dump); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}

	var result *model.DBImportResult
	if dump.Version == 2 {
		result, err = op.DBImportV2(c.Request.Context(), &dump, options)
	} else {
		if options.DryRun || (options.ConflictPolicy != "" && options.ConflictPolicy != model.DBImportConflictReject) {
			resp.ErrorWithCode(c, http.StatusBadRequest, "DB_IMPORT_OPTIONS_INVALID", "dry-run and incremental conflict policies require a version 2 backup")
			return
		}
		result, err = op.DBImportRestore(c.Request.Context(), &dump)
	}
	if err != nil {
		var conflictErr *op.DBImportConflictError
		if errors.As(err, &conflictErr) {
			resp.ErrorWithCodeAndDetails(c, http.StatusConflict, "DB_IMPORT_CONFLICT", conflictErr.Error(), map[string]interface{}{
				"result": conflictErr.Result,
			})
			return
		}
		var validationErr *op.DBImportValidationError
		if errors.As(err, &validationErr) {
			resp.ErrorWithCodeAndDetails(c, http.StatusBadRequest, "DB_IMPORT_VALIDATION_FAILED", validationErr.Error(), map[string]interface{}{
				"field": validationErr.Field,
			})
			return
		}
		log.WithContext(c.Request.Context()).Errorw("database restore failed", "error", err)
		resp.ErrorWithCode(c, http.StatusInternalServerError, "DB_IMPORT_FAILED", "database restore failed")
		return
	}

	if result.DryRun {
		resp.Success(c, result)
		return
	}
	refreshErr := refreshCachesAfterDBImport()
	// The database write is already committed on both paths. Drop every runtime
	// decision and wake in-flight requests even if cache refresh failed, so the
	// process cannot silently keep serving the pre-restore routing generation.
	channelstate.InvalidateAll()
	if refreshErr != nil {
		log.WithContext(c.Request.Context()).Errorw("runtime cache refresh after database restore failed", "error", refreshErr)
		resp.ErrorWithCodeAndDetails(c, http.StatusInternalServerError, "DB_IMPORT_CACHE_REFRESH_FAILED", "database restore committed but runtime cache refresh failed; restart the service before serving traffic", map[string]interface{}{
			"import_committed": true,
			"mode":             result.Mode,
		})
		return
	}
	result.CacheRefreshed = true

	resp.Success(c, result)
}

func parseDBImportOptions(c *gin.Context) (model.DBImportOptions, error) {
	options := model.DBImportOptions{
		ConflictPolicy: model.DBImportConflictPolicy(strings.TrimSpace(c.Query("conflict_policy"))),
	}
	value := strings.TrimSpace(c.Query("dry_run"))
	if value == "" {
		return options, nil
	}
	dryRun, err := strconv.ParseBool(value)
	if err != nil {
		return model.DBImportOptions{}, fmt.Errorf("dry_run must be true or false")
	}
	options.DryRun = dryRun
	return options, nil
}

func readDBImportRequest(c *gin.Context) ([]byte, []byte, error) {
	password := []byte(c.GetHeader(backupPasswordHeader))
	c.Request.Header.Del(backupPasswordHeader)
	contentType, _, mediaTypeErr := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if mediaTypeErr == nil && strings.EqualFold(contentType, "multipart/form-data") {
		maxRequestBytes := conf.MaxDBImportBytes + op.DBBackupEnvelopeOverhead + maxDBImportMultipartOverhead
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBytes)
		if err := c.Request.ParseMultipartForm(maxDBImportMultipartMemory); err != nil {
			clear(password)
			if strings.Contains(err.Error(), "http: request body too large") {
				return nil, nil, op.ErrDBBackupTooLarge
			}
			return nil, nil, errors.New("invalid database backup multipart request")
		}
		if c.Request.MultipartForm != nil {
			defer func() {
				if err := c.Request.MultipartForm.RemoveAll(); err != nil {
					log.WithContext(c.Request.Context()).Warnw("failed to remove database import temporary files", "error", err)
				}
			}()
		}

		passwordValues := c.Request.MultipartForm.Value["password"]
		delete(c.Request.MultipartForm.Value, "password")
		if len(passwordValues) > 1 || (len(passwordValues) == 1 && len(password) != 0) {
			for i := range passwordValues {
				passwordValues[i] = ""
			}
			clear(password)
			return nil, nil, errBackupPasswordDuplicated
		}
		if len(passwordValues) == 1 {
			clear(password)
			password = []byte(passwordValues[0])
			passwordValues[0] = ""
		}

		files := c.Request.MultipartForm.File["file"]
		if len(files) == 0 {
			clear(password)
			return nil, nil, errBackupUploadMissing
		}
		if len(files) != 1 {
			clear(password)
			return nil, nil, errBackupUploadDuplicated
		}
		maxEncodedBytes := conf.MaxDBImportBytes + op.DBBackupEnvelopeOverhead
		if files[0].Size > maxEncodedBytes {
			clear(password)
			return nil, nil, op.ErrDBBackupTooLarge
		}
		file, err := files[0].Open()
		if err != nil {
			clear(password)
			return nil, nil, errors.New("could not open database backup upload")
		}
		body, readErr := io.ReadAll(io.LimitReader(file, maxEncodedBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			clear(body)
			clear(password)
			return nil, nil, errors.New("could not read database backup upload")
		}
		if closeErr != nil {
			clear(body)
			clear(password)
			log.WithContext(c.Request.Context()).Warnw("failed to close database import upload", "error", closeErr)
			return nil, nil, errors.New("could not close database backup upload")
		}
		if int64(len(body)) > maxEncodedBytes {
			clear(body)
			clear(password)
			return nil, nil, op.ErrDBBackupTooLarge
		}
		return body, password, nil
	}

	maxEncodedBytes := conf.MaxDBImportBytes + op.DBBackupEnvelopeOverhead
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxEncodedBytes)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		clear(password)
		if strings.Contains(err.Error(), "http: request body too large") {
			return nil, nil, op.ErrDBBackupTooLarge
		}
		return nil, nil, errors.New("could not read database backup request")
	}
	return body, password, nil
}

func respondDBBackupInputError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, op.ErrDBBackupTooLarge):
		resp.ErrorWithCode(c, http.StatusRequestEntityTooLarge, "DB_BACKUP_TOO_LARGE", op.ErrDBBackupTooLarge.Error())
	case errors.Is(err, op.ErrDBBackupPasswordRequired):
		resp.ErrorWithCode(c, http.StatusBadRequest, "DB_BACKUP_PASSWORD_REQUIRED", op.ErrDBBackupPasswordRequired.Error())
	case errors.Is(err, op.ErrDBBackupPasswordInvalid):
		resp.ErrorWithCode(c, http.StatusBadRequest, "DB_BACKUP_PASSWORD_INVALID", op.ErrDBBackupPasswordInvalid.Error())
	case errors.Is(err, op.ErrDBBackupAuthentication):
		resp.ErrorWithCode(c, http.StatusBadRequest, "DB_BACKUP_DECRYPT_FAILED", op.ErrDBBackupAuthentication.Error())
	case errors.Is(err, op.ErrDBBackupInvalidEnvelope),
		errors.Is(err, op.ErrDBBackupUnsupportedEnvelope),
		errors.Is(err, op.ErrDBBackupUnsupportedKDFParams):
		resp.ErrorWithCode(c, http.StatusBadRequest, "DB_BACKUP_INVALID_ENVELOPE", "invalid or unsupported encrypted database backup")
	default:
		resp.Error(c, http.StatusBadRequest, err.Error())
	}
}

func decodeDBDump(body []byte, dump *model.DBDump) error {
	if dump == nil {
		var value any
		return decodeStrictJSON(body, &value)
	}

	var probe map[string]json.RawMessage
	if err := decodeStrictJSON(body, &probe); err != nil {
		return err
	}
	if _, wrapped := probe["data"]; wrapped {
		var wrapper struct {
			Code      int             `json:"code"`
			Message   string          `json:"message"`
			Data      json.RawMessage `json:"data"`
			RequestID string          `json:"request_id"`
		}
		if err := decodeStrictJSON(body, &wrapper); err != nil {
			return err
		}
		if wrapper.Code != 0 && wrapper.Code != http.StatusOK {
			return fmt.Errorf("wrapped database dump has unsuccessful code %d", wrapper.Code)
		}
		if len(wrapper.Data) == 0 || bytes.Equal(bytes.TrimSpace(wrapper.Data), []byte("null")) {
			return errors.New("wrapped database dump is missing data")
		}
		return decodeStrictJSON(wrapper.Data, dump)
	}
	return decodeStrictJSON(body, dump)
}

func decodeStrictJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}
