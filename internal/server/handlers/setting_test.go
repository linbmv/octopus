package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/routingstate"
	projectlog "github.com/bestruirui/octopus/internal/utils/log"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type importOversizedReader struct {
	remaining int64
}

func (r *importOversizedReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:int(r.remaining)]
	}
	for i := range p {
		p[i] = ' '
	}
	r.remaining -= int64(len(p))
	return len(p), nil
}

func TestImportDBRejectsOversizedRawBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.POST("/import", importDB)

	req := httptest.NewRequest(http.MethodPost, "/import", &importOversizedReader{remaining: conf.MaxDBImportBytes + 1})
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusRequestEntityTooLarge, w.Body.String())
	}
}

func TestExportDBEncryptedEnvelopeHeadersAndRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "encrypted-export.db"), false); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	const password = "handler export password"
	router := gin.New()
	router.GET("/export", exportDB)
	req := httptest.NewRequest(http.MethodGet, "/export?include_logs=false&include_stats=false", nil)
	req.Header.Set(backupPasswordHeader, password)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != op.EncryptedDBBackupContentType {
		t.Fatalf("Content-Type = %q, want %q", got, op.EncryptedDBBackupContentType)
	}
	if got := w.Header().Get("Content-Disposition"); !strings.Contains(got, op.EncryptedDBBackupExtension+"\"") {
		t.Fatalf("Content-Disposition = %q, want encrypted extension", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := req.Header.Get(backupPasswordHeader); got != "" {
		t.Fatalf("password header remained attached to request after handler: %q", got)
	}
	if strings.Contains(w.Body.String(), password) {
		t.Fatal("encrypted response exposed the backup password")
	}
	plaintext, err := op.DecodeDBBackup(w.Body.Bytes(), []byte(password), conf.MaxDBExportBytes)
	if err != nil {
		t.Fatalf("decrypt handler export: %v", err)
	}
	var dump model.DBDump
	if err := decodeDBDump(plaintext, &dump); err != nil {
		t.Fatalf("decode handler export: %v", err)
	}
	if dump.Version != 2 || dump.ExportedAt.IsZero() || dump.IncludeLogs || dump.IncludeStats || dump.Relations == nil {
		t.Fatalf("exported dump metadata = %#v", dump)
	}
}

func TestExportDBPlaintextSpoolsBeforeSuccessfulHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "plaintext-export.db"), false); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	t.Run("complete export has an exact detectable length", func(t *testing.T) {
		router := gin.New()
		router.GET("/export", exportDB)
		req := httptest.NewRequest(http.MethodGet, "/export", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
		}
		if got, want := w.Header().Get("Content-Length"), strconv.Itoa(w.Body.Len()); got != want {
			t.Fatalf("Content-Length = %q, want %q", got, want)
		}
		var dump model.DBDump
		if err := decodeDBDump(w.Body.Bytes(), &dump); err != nil {
			t.Fatalf("decode complete plaintext export: %v", err)
		}
		if dump.Version != 2 || dump.ExportedAt.IsZero() || dump.Relations == nil {
			t.Fatalf("plaintext dump metadata = %#v", dump)
		}
	})

	t.Run("limit failure is returned before attachment headers", func(t *testing.T) {
		oldLimit := dbExportByteLimit
		dbExportByteLimit = 1
		t.Cleanup(func() { dbExportByteLimit = oldLimit })

		router := gin.New()
		router.GET("/export", exportDB)
		req := httptest.NewRequest(http.MethodGet, "/export", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusRequestEntityTooLarge, w.Body.String())
		}
		if disposition := w.Header().Get("Content-Disposition"); disposition != "" {
			t.Fatalf("failed export emitted attachment header %q", disposition)
		}
		var response struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("failed export did not return complete error JSON: %v; body=%q", err, w.Body.String())
		}
		if response.Error.Code != "DB_BACKUP_TOO_LARGE" {
			t.Fatalf("response code = %q, want DB_BACKUP_TOO_LARGE", response.Error.Code)
		}
	})
}

func TestImportDBEncryptedWrongPasswordDoesNotLeakSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const correctPassword = "correct handler password"
	const wrongPassword = "wrong handler password"
	encrypted, err := op.EncryptDBBackup([]byte(`{"version":0}`), []byte(correctPassword), 1024)
	if err != nil {
		t.Fatalf("EncryptDBBackup() error = %v", err)
	}

	var capturedLogs bytes.Buffer
	oldLogger := projectlog.Logger
	encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	projectlog.Logger = zap.New(zapcore.NewCore(encoder, zapcore.AddSync(&capturedLogs), zap.DebugLevel)).Sugar()
	t.Cleanup(func() { projectlog.Logger = oldLogger })

	router := gin.New()
	router.POST("/import", importDB)
	req := httptest.NewRequest(http.MethodPost, "/import", bytes.NewReader(encrypted))
	req.Header.Set("Content-Type", op.EncryptedDBBackupContentType)
	req.Header.Set(backupPasswordHeader, wrongPassword)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error.Code != "DB_BACKUP_DECRYPT_FAILED" {
		t.Fatalf("response code = %q, want DB_BACKUP_DECRYPT_FAILED", response.Error.Code)
	}
	for _, secret := range []string{correctPassword, wrongPassword} {
		if strings.Contains(w.Body.String(), secret) || strings.Contains(capturedLogs.String(), secret) {
			t.Fatalf("handler response or logs exposed backup password %q", secret)
		}
	}
	if req.Header.Get(backupPasswordHeader) != "" {
		t.Fatal("password header remained attached to request after failed import")
	}
}

func TestImportDBEncryptedRawBodyRestoresAndRefreshes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "encrypted-import.db"), false); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	oldRefresh := refreshCachesAfterDBImport
	refreshCachesAfterDBImport = func() error { return nil }
	t.Cleanup(func() { refreshCachesAfterDBImport = oldRefresh })

	dump := model.DBDump{
		Version:    1,
		ExportedAt: time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC),
		Channels: []model.Channel{{
			ID:       201,
			Name:     "encrypted-handler-import",
			Type:     llm.APIFormatOpenAIChatCompletion,
			Enabled:  true,
			BaseUrls: []model.BaseUrl{{URL: "https://api.example.test/v1"}},
			Model:    "encrypted-handler-model",
		}},
		ChannelKeys: []model.ChannelKey{{ID: 202, ChannelID: 201, Enabled: true, ChannelKey: "sk-encrypted-handler"}},
	}
	plaintext, err := json.Marshal(dump)
	if err != nil {
		t.Fatalf("marshal dump: %v", err)
	}
	const password = "encrypted raw handler password"
	encrypted, err := op.EncryptDBBackup(plaintext, []byte(password), conf.MaxDBImportBytes)
	if err != nil {
		t.Fatalf("EncryptDBBackup() error = %v", err)
	}

	router := gin.New()
	router.POST("/import", importDB)
	routingBefore := routingstate.Current()
	req := httptest.NewRequest(http.MethodPost, "/import", bytes.NewReader(encrypted))
	req.Header.Set("Content-Type", op.EncryptedDBBackupContentType)
	req.Header.Set(backupPasswordHeader, password)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if strings.Contains(w.Body.String(), password) {
		t.Fatal("successful encrypted import exposed the backup password")
	}
	var imported model.Channel
	if err := db.GetDB().First(&imported, 201).Error; err != nil {
		t.Fatalf("load encrypted handler import: %v", err)
	}
	if imported.Name != dump.Channels[0].Name {
		t.Fatalf("imported channel = %#v", imported)
	}
	select {
	case <-routingBefore.Changed:
	default:
		t.Fatal("successful database import did not publish a routing change")
	}
}

func TestImportDBV2DryRunAndConflictReport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "v2-import-handler.db"), false); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	refreshCalls := 0
	oldRefresh := refreshCachesAfterDBImport
	refreshCachesAfterDBImport = func() error {
		refreshCalls++
		return nil
	}
	t.Cleanup(func() { refreshCachesAfterDBImport = oldRefresh })

	dump := model.DBDump{
		Version:    2,
		ExportedAt: time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC),
		Channels: []model.Channel{{
			ID:       101,
			UUID:     "00000000-0000-4000-8000-000000000101",
			Name:     "handler-v2-channel",
			Type:     llm.APIFormatOpenAIChatCompletion,
			Enabled:  true,
			BaseUrls: []model.BaseUrl{{URL: "https://v2.example.test/v1"}},
			Model:    "handler-v2-model",
		}},
		ChannelKeys: []model.ChannelKey{{
			ID: 102, UUID: "00000000-0000-4000-8000-000000000102", ChannelID: 101,
			Enabled: true, ChannelKey: "sk-handler-v2",
		}},
		Relations: &model.DBDumpRelationsV2{
			ChannelKeys: map[string]string{
				"00000000-0000-4000-8000-000000000102": "00000000-0000-4000-8000-000000000101",
			},
			GroupItems: map[string]model.DBDumpGroupItemRelation{},
		},
	}
	body, err := json.Marshal(dump)
	if err != nil {
		t.Fatalf("marshal v2 dump: %v", err)
	}
	router := gin.New()
	router.POST("/import", importDB)

	dryRunRequest := httptest.NewRequest(http.MethodPost, "/import?dry_run=true&conflict_policy=replace", bytes.NewReader(body))
	dryRunRequest.Header.Set("Content-Type", "application/json")
	dryRunResponse := httptest.NewRecorder()
	router.ServeHTTP(dryRunResponse, dryRunRequest)
	if dryRunResponse.Code != http.StatusOK {
		t.Fatalf("dry-run status = %d, want %d; body=%s", dryRunResponse.Code, http.StatusOK, dryRunResponse.Body.String())
	}
	var success struct {
		Data model.DBImportResult `json:"data"`
	}
	if err := json.Unmarshal(dryRunResponse.Body.Bytes(), &success); err != nil {
		t.Fatalf("decode dry-run response: %v", err)
	}
	if !success.Data.DryRun || success.Data.ConflictPolicy != model.DBImportConflictReplace || success.Data.Tables["channels"].Create != 1 {
		t.Fatalf("dry-run response = %#v", success.Data)
	}
	if refreshCalls != 0 {
		t.Fatalf("dry-run refreshed caches %d times", refreshCalls)
	}
	var channelCount int64
	if err := db.GetDB().Model(&model.Channel{}).Count(&channelCount).Error; err != nil || channelCount != 0 {
		t.Fatalf("dry-run channel count = %d, err=%v", channelCount, err)
	}

	existing := dump.Channels[0]
	existing.ID = 0
	if err := db.GetDB().Omit("Keys", "Stats").Create(&existing).Error; err != nil {
		t.Fatalf("create conflicting channel: %v", err)
	}
	conflictRequest := httptest.NewRequest(http.MethodPost, "/import?conflict_policy=reject", bytes.NewReader(body))
	conflictRequest.Header.Set("Content-Type", "application/json")
	conflictResponse := httptest.NewRecorder()
	router.ServeHTTP(conflictResponse, conflictRequest)
	if conflictResponse.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, want %d; body=%s", conflictResponse.Code, http.StatusConflict, conflictResponse.Body.String())
	}
	var conflict struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Result model.DBImportResult `json:"result"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(conflictResponse.Body.Bytes(), &conflict); err != nil {
		t.Fatalf("decode conflict response: %v", err)
	}
	if conflict.Error.Code != "DB_IMPORT_CONFLICT" || conflict.Error.Details.Result.Tables["channels"].Conflict != 1 {
		t.Fatalf("conflict response = %#v", conflict)
	}
	if refreshCalls != 0 {
		t.Fatalf("rejected import refreshed caches %d times", refreshCalls)
	}
}

func TestImportDBEncryptedMultipartPasswordField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const password = "multipart backup password"
	encrypted, err := op.EncryptDBBackup([]byte(`{"version":0}`), []byte(password), 1024)
	if err != nil {
		t.Fatalf("EncryptDBBackup() error = %v", err)
	}

	var requestBody bytes.Buffer
	multipartWriter := multipart.NewWriter(&requestBody)
	fileWriter, err := multipartWriter.CreateFormFile("file", "backup"+op.EncryptedDBBackupExtension)
	if err != nil {
		t.Fatalf("CreateFormFile() error = %v", err)
	}
	if _, err := fileWriter.Write(encrypted); err != nil {
		t.Fatalf("write encrypted multipart file: %v", err)
	}
	if err := multipartWriter.WriteField("password", password); err != nil {
		t.Fatalf("write multipart password: %v", err)
	}
	if err := multipartWriter.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	router := gin.New()
	router.POST("/import", importDB)
	req := httptest.NewRequest(http.MethodPost, "/import", &requestBody)
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error.Code != "DB_IMPORT_VALIDATION_FAILED" {
		t.Fatalf("response code = %q, want DB_IMPORT_VALIDATION_FAILED; body=%s", response.Error.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), password) {
		t.Fatal("multipart password was exposed in response")
	}
	if req.MultipartForm != nil {
		if _, retained := req.MultipartForm.Value["password"]; retained {
			t.Fatal("multipart password remained in parsed form after handler")
		}
	}
}

func TestBackupHandlersRejectPasswordInURLWithoutEchoingIt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const password = "must-not-enter-a-url"
	for _, test := range []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{method: http.MethodGet, path: "/export?password=" + password, handler: exportDB},
		{method: http.MethodPost, path: "/import?password=" + password, handler: importDB},
	} {
		t.Run(test.method, func(t *testing.T) {
			router := gin.New()
			router.Handle(test.method, strings.Split(test.path, "?")[0], test.handler)
			req := httptest.NewRequest(test.method, test.path, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusBadRequest, w.Body.String())
			}
			if strings.Contains(w.Body.String(), password) {
				t.Fatal("URL password was echoed in response")
			}
		})
	}
}

func TestDecodeDBDumpRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	tests := map[string]string{
		"unknown direct field":  `{"version":1,"exported_at":"2026-07-15T12:00:00Z","unexpected":true}`,
		"trailing direct value": `{"version":1,"exported_at":"2026-07-15T12:00:00Z"}{}`,
		"unknown wrapper field": `{"code":200,"data":{"version":1,"exported_at":"2026-07-15T12:00:00Z"},"unexpected":true}`,
		"unknown nested field":  `{"code":200,"data":{"version":1,"exported_at":"2026-07-15T12:00:00Z","unexpected":true}}`,
		"failed wrapper":        `{"code":500,"data":{"version":1,"exported_at":"2026-07-15T12:00:00Z"}}`,
		"trailing wrapped value": `{"code":200,"data":{"version":1,"exported_at":"2026-07-15T12:00:00Z"}}
			[]`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			var dump model.DBDump
			if err := decodeDBDump([]byte(body), &dump); err == nil {
				t.Fatalf("decodeDBDump(%s) error = nil", body)
			}
		})
	}
}

func TestImportDBMapsTypedValidationErrorToBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/import", importDB)
	req := httptest.NewRequest(http.MethodPost, "/import", bytes.NewBufferString(`{"version":0}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	var response struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error.Code != "DB_IMPORT_VALIDATION_FAILED" || response.Error.Details["field"] != "version" {
		t.Fatalf("response error = %#v", response.Error)
	}
}

func TestImportDBReportsCommittedCacheRefreshFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "import-handler.db"), false); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	oldRefresh := refreshCachesAfterDBImport
	refreshCachesAfterDBImport = func() error { return errors.New("injected cache refresh failure") }
	t.Cleanup(func() { refreshCachesAfterDBImport = oldRefresh })
	routingBefore := routingstate.Current()

	dump := model.DBDump{
		Version:    1,
		ExportedAt: time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC),
		Channels: []model.Channel{{
			ID:       101,
			Name:     "handler-import",
			Type:     llm.APIFormatOpenAIChatCompletion,
			Enabled:  true,
			BaseUrls: []model.BaseUrl{{URL: "https://api.example.test/v1"}},
			Model:    "handler-model",
		}},
		ChannelKeys: []model.ChannelKey{{ID: 102, ChannelID: 101, Enabled: true, ChannelKey: "sk-handler"}},
	}
	body, err := json.Marshal(dump)
	if err != nil {
		t.Fatalf("marshal dump: %v", err)
	}
	router := gin.New()
	router.POST("/import", importDB)
	req := httptest.NewRequest(http.MethodPost, "/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
	select {
	case <-routingBefore.Changed:
	default:
		t.Fatal("committed database import did not publish a routing change after cache refresh failure")
	}
	var response struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error.Code != "DB_IMPORT_CACHE_REFRESH_FAILED" || response.Error.Details["import_committed"] != true {
		t.Fatalf("response error = %#v", response.Error)
	}
	var count int64
	if err := db.GetDB().Model(&model.Channel{}).Where("id = ?", 101).Count(&count).Error; err != nil {
		t.Fatalf("count committed channel: %v", err)
	}
	if count != 1 {
		t.Fatalf("committed channel count = %d, want 1", count)
	}
}
