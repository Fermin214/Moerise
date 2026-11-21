package moerise

import (
	"encoding/json"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
)

type openAPIParam struct {
	Name        string
	In          string
	Required    bool
	Description string
	Schema      map[string]any
}

type openAPIOp struct {
	Method      string
	Path        string
	OperationID string
	Security    []map[string][]string
	Parameters  []openAPIParam
	RequestRef  string
	Responses   map[string]any
}

type openAPIErrorDetail struct {
	Code         string   `json:"code"`
	Message      string   `json:"message"`
	Retryable    bool     `json:"retryable"`
	RequestID    string   `json:"request_id"`
	ChangedNames []string `json:"changed_names,omitempty"`
}

type openAPIErrorResponse struct {
	Error openAPIErrorDetail `json:"error"`
}

type openAPILiveness struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

type openAPIReadiness struct {
	Status string `json:"status"`
}

type openAPIIdentity struct {
	InstanceID string   `json:"instance_id"`
	User       string   `json:"user"`
	Credential string   `json:"credential"`
	Scopes     []string `json:"scopes"`
	Timezone   string   `json:"timezone"`
	APIVersion string   `json:"api_version"`
}

type openAPICapabilities struct {
	Version      string         `json:"version"`
	Telemetry    map[string]any `json:"telemetry"`
	Chat         map[string]any `json:"chat"`
	Sync         map[string]any `json:"sync"`
	LoginMethods []string       `json:"login_methods"`
	Notes        string         `json:"notes"`
}

type openAPIChatPage struct {
	Chats          []ChatSummary `json:"chats"`
	NextPageToken  string        `json:"next_page_token"`
	CharacterLimit int           `json:"character_limit"`
}

type openAPIChatMaterialPage struct {
	Notes          []Note `json:"notes"`
	NextPageToken  string `json:"next_page_token"`
	CharacterLimit int    `json:"character_limit"`
}

type openAPIChatCandidatePage struct {
	Candidates     []ChatCandidate `json:"candidates"`
	NextPageToken  string          `json:"next_page_token"`
	CharacterLimit int             `json:"character_limit"`
	SourceLimit    int             `json:"source_limit"`
}

type openAPIChatNames struct {
	Names []string `json:"names"`
}

type openAPIChatVersion struct {
	Version   int64          `json:"version"`
	Materials []ChatMaterial `json:"materials,omitempty"`
}

type openAPIChatStreamError struct {
	Error openAPIErrorDetail `json:"error"`
	Turn  *ChatTurn          `json:"turn"`
}

type openAPIClientTags struct {
	Tags []TagCount `json:"tags"`
}

type openAPIClientCalendar struct {
	Month string     `json:"month"`
	Days  []DayCount `json:"days"`
}

type openAPINotificationList struct {
	Notifications []Notification `json:"notifications"`
}

type openAPIJobResponse struct {
	Job Job `json:"job"`
}

type openAPIWalkNext struct {
	Name string   `json:"name"`
	Path []string `json:"path,omitempty"`
}

type openAPIWalkStart struct {
	Name *string `json:"name,omitempty"`
}

type openAPIDeleteNote struct {
	SourceHash string `json:"source_hash"`
}

type openAPIModelCheckRequest struct {
	Version int64   `json:"version"`
	Model   *string `json:"model,omitempty"`
}

type openAPISettingsSecretUpdates struct {
	QQAppSecret       *SecretUpdate `json:"qq_app_secret,omitempty"`
	WebhookAppSecret  *SecretUpdate `json:"webhook_app_secret,omitempty"`
	LLMAPIKey         *SecretUpdate `json:"llm_api_key,omitempty"`
	EmbeddingAPIKey   *SecretUpdate `json:"embedding_api_key,omitempty"`
	AppSecret         *SecretUpdate `json:"app_secret,omitempty"`
	VerificationToken *SecretUpdate `json:"verification_token,omitempty"`
	EncryptKey        *SecretUpdate `json:"encrypt_key,omitempty"`
	WebhookURL        *SecretUpdate `json:"webhook_url,omitempty"`
	WebhookSecret     *SecretUpdate `json:"webhook_secret,omitempty"`
	BotToken          *SecretUpdate `json:"bot_token,omitempty"`
}

var openAPITypeOptions = &jsonschema.ForOptions{
	TypeSchemas: map[reflect.Type]*jsonschema.Schema{
		reflect.TypeFor[json.RawMessage](): {},
		reflect.TypeFor[[]byte]():          {Type: "string", Format: "byte"},
	},
}

func schemaFrom[T any]() json.RawMessage {
	schema, err := jsonschema.For[T](openAPITypeOptions)
	if err != nil {
		panic(err)
	}
	return normalizeSchemaObject(schema)
}

func normalizeSchemaObject(schema *jsonschema.Schema) json.RawMessage {
	data, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return json.RawMessage(data)
}

func openAPIRefSchema(name string) json.RawMessage {
	data, _ := json.Marshal(map[string]any{"$ref": "#/components/schemas/" + name})
	return json.RawMessage(data)
}

func buildOpenAPISchemas() map[string]json.RawMessage {
	anySchema := json.RawMessage(`{}`)
	clientNoteRef := openAPIRefSchema("Note")
	clientNotePageRef := openAPIRefSchema("NotePage")
	schemas := map[string]json.RawMessage{
		"AttachmentReceipt":       schemaFrom[AttachmentReceipt](),
		"ChatCandidate":           schemaFrom[ChatCandidate](),
		"ChatCandidatePage":       schemaFrom[openAPIChatCandidatePage](),
		"ChatCreate":              schemaFrom[ChatCreate](),
		"ChatEvent":               schemaFrom[ChatEvent](),
		"ChatGenerate":            schemaFrom[ChatGenerate](),
		"ChatMaterial":            schemaFrom[ChatMaterial](),
		"ChatMaterialPage":        schemaFrom[openAPIChatMaterialPage](),
		"ChatMaterials":           schemaFrom[ChatMaterials](),
		"ChatMessage":             schemaFrom[ChatMessage](),
		"ChatMessageRef":          schemaFrom[ChatMessageRef](),
		"ChatNames":               schemaFrom[openAPIChatNames](),
		"ChatOperationKey":        json.RawMessage(`{"type":"string","minLength":1,"maxLength":128}`),
		"ChatPage":                schemaFrom[openAPIChatPage](),
		"ChatSaveReceipt":         schemaFrom[ChatSaveReceipt](),
		"ChatSaveSelection":       schemaFrom[ChatSaveSelection](),
		"ChatSession":             schemaFrom[ChatSession](),
		"ChatStreamError":         schemaFrom[openAPIChatStreamError](),
		"ChatSummary":             schemaFrom[ChatSummary](),
		"ChatTurn":                schemaFrom[ChatTurn](),
		"ChatUpdate":              schemaFrom[ChatUpdate](),
		"ChatVersion":             schemaFrom[openAPIChatVersion](),
		"ClientAttachment":        schemaFrom[map[string]any](),
		"ClientAttachmentInput":   schemaFrom[CreateAttachment](),
		"ClientCalendar":          schemaFrom[openAPIClientCalendar](),
		"ClientCapabilities":      schemaFrom[openAPICapabilities](),
		"ClientCreateNote":        schemaFrom[ClientCreateNote](),
		"ClientDayCount":          schemaFrom[DayCount](),
		"ClientDeleteNote":        schemaFrom[openAPIDeleteNote](),
		"ClientLogin":             schemaFrom[ClientLogin](),
		"ClientModelCheck":        schemaFrom[ClientModelCheck](),
		"ClientModelCheckRequest": schemaFrom[openAPIModelCheckRequest](),
		"ClientModelStatus":       schemaFrom[ClientModelStatus](),
		"ClientModels":            schemaFrom[ClientModels](),
		"ClientNote":              clientNoteRef,
		"ClientNotePage":          clientNotePageRef,
		"ClientProductSettings":   schemaFrom[ProductSettings](),
		"ClientReviewBatch":       openAPIRefSchema("ReviewBatch"),
		"ClientReviewItem":        openAPIRefSchema("ReviewItem"),
		"ClientSearchHit":         openAPIRefSchema("SearchHit"),
		"ClientSearchPage":        schemaFrom[ClientSearchPage](),
		"ClientSearchRequest":     schemaFrom[ClientSearchRequest](),
		"ClientSession":           schemaFrom[SessionIdentity](),
		"ClientSettingsPatch":     json.RawMessage(`{"type":"object","additionalProperties":true}`),
		"ClientTagCount":          schemaFrom[TagCount](),
		"ClientTags":              schemaFrom[openAPIClientTags](),
		"ClientUpdateNote":        schemaFrom[UpdateNote](),
		"ClientWalkNext":          schemaFrom[openAPIWalkNext](),
		"ClientWalkResponse":      openAPIRefSchema("WalkResponse"),
		"ClientWalkStart":         schemaFrom[openAPIWalkStart](),
		"CreateAttachmentRequest": schemaFrom[CreateAttachment](),
		"ErrorDetail":             schemaFrom[openAPIErrorDetail](),
		"ErrorResponse":           schemaFrom[openAPIErrorResponse](),
		"Identity":                schemaFrom[openAPIIdentity](),
		"Job":                     schemaFrom[Job](),
		"JobCount":                schemaFrom[JobCount](),
		"JobResponse":             schemaFrom[openAPIJobResponse](),
		"LLMUsage":                schemaFrom[LLMUsage](),
		"Liveness":                schemaFrom[openAPILiveness](),
		"MapDistrict":             schemaFrom[MapDistrict](),
		"MapLink":                 schemaFrom[MapLink](),
		"MapPoint":                schemaFrom[MapPoint](),
		"MapResponse":             schemaFrom[MapResponse](),
		"MapStellar":              schemaFrom[MapStellar](),
		"MapTopic":                schemaFrom[MapTopic](),
		"Note":                    schemaFrom[Note](),
		"NotePage":                schemaFrom[NotePage](),
		"Notification":            schemaFrom[Notification](),
		"NotificationDetail":      schemaFrom[NotificationDetail](),
		"EmptyRequest":            schemaFrom[struct{}](),
		"NotificationList":        schemaFrom[openAPINotificationList](),
		"PrivateBotStatus":        schemaFrom[botStatus](),
		"Readiness":               schemaFrom[openAPIReadiness](),
		"RelatedRequest":          schemaFrom[RelatedRequest](),
		"RelatedResponse":         schemaFrom[RelatedResponse](),
		"ReviewBatch":             schemaFrom[ReviewBatch](),
		"ReviewItem":              schemaFrom[ReviewItem](),
		"SearchHit":               schemaFrom[SearchHit](),
		"SearchMatch":             schemaFrom[SearchMatch](),
		"SearchRequest":           schemaFrom[SearchRequest](),
		"SearchResponse":          schemaFrom[SearchResponse](),
		"SearchSemantic":          schemaFrom[SearchSemantic](),
		"SettingsSecretOperation": schemaFrom[SecretUpdate](),
		"SettingsSecretStatus":    schemaFrom[SecretStatus](),
		"SettingsSecretUpdates":   schemaFrom[openAPISettingsSecretUpdates](),
		"SettingsUpdate":          schemaFrom[SettingsUpdate](),
		"SettingsView":            schemaFrom[SettingsView](),
		"SyncNotesResponse":       schemaFrom[SyncNotesResponse](),
		"SyncStatus":              schemaFrom[SyncStatus](),
		"VectorGeneration":        schemaFrom[VectorGeneration](),
		"VectorIndexStatus":       schemaFrom[VectorIndexStatus](),
		"WalkRequest":             schemaFrom[WalkRequest](),
		"WalkResponse":            schemaFrom[WalkResponse](),
	}
	var product map[string]any
	if err := json.Unmarshal(schemas["ClientProductSettings"], &product); err == nil {
		product["additionalProperties"] = true
		if raw, err := json.Marshal(product); err == nil {
			schemas["ClientProductSettings"] = raw
		}
	}
	_ = anySchema
	return schemas
}

func buildOpenAPISpec() []byte {
	extSec := []map[string][]string{{"HTTPBearer": {}}}
	appSec := []map[string][]string{{"BrowserSession": {}}, {"LocalBrowserSession": {}}}
	publicSec := []map[string][]string{}
	queryParam := func(name, kind, def string) openAPIParam {
		schema := map[string]any{"type": kind}
		if def != "" {
			schema["default"] = def
		}
		return openAPIParam{Name: name, In: "query", Schema: schema}
	}
	pathParam := func(name string) openAPIParam {
		return openAPIParam{Name: name, In: "path", Required: true, Schema: map[string]any{"type": "string"}}
	}
	headerParam := func(name string, required bool) openAPIParam {
		return openAPIParam{Name: name, In: "header", Required: required, Schema: map[string]any{"type": "string"}}
	}

	jsonResp := func(desc, ref string) map[string]any {
		return map[string]any{
			"description": desc,
			"content": map[string]any{
				"application/json": map[string]any{"schema": map[string]any{"$ref": ref}},
			},
		}
	}
	schemaResp := func(desc, media string, schema map[string]any) map[string]any {
		return map[string]any{
			"description": desc,
			"content": map[string]any{
				media: map[string]any{"schema": schema},
			},
		}
	}
	descResp := func(desc string) map[string]any {
		return map[string]any{"description": desc}
	}
	stdResponses := func(okCode, okSchema string, codes ...string) map[string]any {
		resps := map[string]any{}
		if okSchema != "" {
			resps[okCode] = jsonResp("OK", okSchema)
		} else {
			resps[okCode] = descResp("OK")
		}
		for _, code := range codes {
			resps[code] = jsonResp(code, "#/components/schemas/ErrorResponse")
		}
		return resps
	}
	noteWriteResponses := func(okCodes []string, okSchema string, codes ...string) map[string]any {
		resps := map[string]any{}
		for _, code := range okCodes {
			if okSchema != "" {
				resps[code] = jsonResp(code, okSchema)
			} else {
				resps[code] = descResp(code)
			}
		}
		for _, code := range codes {
			resps[code] = jsonResp(code, "#/components/schemas/ErrorResponse")
		}
		return resps
	}
	chatErrRef := map[string]any{"$ref": "#/components/responses/ChatError"}
	chatResponses := func(okCodes []string, okMedia string, okSchema map[string]any, codes ...string) map[string]any {
		resps := map[string]any{}
		for _, code := range okCodes {
			if okSchema != nil {
				resps[code] = schemaResp(code, okMedia, okSchema)
			} else {
				resps[code] = descResp(code)
			}
		}
		for _, code := range codes {
			resps[code] = chatErrRef
		}
		return resps
	}
	chatJSON := func(schema string) map[string]any {
		return map[string]any{"$ref": "#/components/schemas/" + schema}
	}
	feishuOK := schemaResp("OK", "application/json", map[string]any{
		"oneOf": []map[string]any{
			{"type": "object", "required": []string{"challenge"}, "properties": map[string]any{"challenge": map[string]any{"type": "string"}}},
			{"type": "object", "required": []string{"code"}, "properties": map[string]any{"code": map[string]any{"type": "integer", "const": 0}}},
		},
	})
	chatMaterialsOK := schemaResp("OK", "application/json", map[string]any{
		"oneOf": []map[string]any{
			{"$ref": "#/components/schemas/ChatMaterialPage"},
			{"$ref": "#/components/schemas/ChatCandidatePage"},
		},
	})
	mapRebuildOK := schemaResp("Queued", "application/json", map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"status": map[string]any{"type": "string", "const": "queued"}},
		"required":             []string{"status"},
		"additionalProperties": false,
	})

	ops := []openAPIOp{
		{"GET", "/health/live", "live_health_live_get", nil, nil, "", stdResponses("200", "#/components/schemas/Liveness", "500")},
		{"GET", "/health/ready", "ready_health_ready_get", nil, nil, "", map[string]any{
			"200": jsonResp("Ready", "#/components/schemas/Readiness"),
			"500": jsonResp("500", "#/components/schemas/ErrorResponse"),
			"503": jsonResp("Degraded", "#/components/schemas/Readiness"),
		}},
		{"GET", "/api/ext/v1/me", "me_api_ext_v1_me_get", extSec, nil, "", stdResponses("200", "#/components/schemas/Identity", "401", "403", "404", "422", "500", "503")},
		{"GET", "/api/ext/v1/notes", "notes_api_ext_v1_notes_get", extSec, []openAPIParam{
			queryParam("state", "string", "NORMAL"), queryParam("page_size", "integer", "20"), queryParam("page_token", "string", ""),
		}, "", stdResponses("200", "#/components/schemas/NotePage", "401", "403", "404", "422", "500", "503")},
		{"GET", "/api/ext/v1/notes/{memo_id}", "note_api_ext_v1_notes__memo_id__get", extSec, []openAPIParam{pathParam("memo_id")}, "", stdResponses("200", "#/components/schemas/Note", "401", "403", "404", "422", "500", "503")},
		{"GET", "/api/ext/v1/notes/{memo_id}/related", "related_api_ext_v1_notes_memo_id_related_get", extSec, []openAPIParam{
			pathParam("memo_id"), queryParam("tags", "string", ""), queryParam("state", "string", "NORMAL"), queryParam("limit", "integer", "10"),
		}, "", stdResponses("200", "#/components/schemas/RelatedResponse", "401", "403", "404", "405", "409", "422", "500", "503")},
		{"GET", "/api/ext/v1/sync", "sync_status_api_ext_v1_sync_get", extSec, nil, "", stdResponses("200", "#/components/schemas/SyncStatus", "401", "403", "404", "422", "500", "503")},
		{"GET", "/api/ext/v1/jobs/{job_id}", "job_api_ext_v1_jobs__job_id__get", extSec, []openAPIParam{pathParam("job_id")}, "", stdResponses("200", "#/components/schemas/JobResponse", "401", "403", "404", "422", "500", "503")},
		{"GET", "/api/ext/v1/search/index", "search_index_api_ext_v1_search_index_get", extSec, nil, "", stdResponses("200", "#/components/schemas/VectorIndexStatus", "401", "403", "405", "500")},
		{"POST", "/api/ext/v1/search", "search_api_ext_v1_search_post", extSec, nil, "#/components/schemas/SearchRequest", stdResponses("200", "#/components/schemas/SearchResponse", "401", "403", "405", "422", "500", "503")},
		{"POST", "/api/ext/v1/discovery/walk", "walk_api_ext_v1_discovery_walk_post", extSec, nil, "#/components/schemas/WalkRequest", stdResponses("200", "#/components/schemas/WalkResponse", "401", "403", "404", "405", "409", "422", "503")},
		{"GET", "/api/ext/v1/reviews/daily", "daily_review_api_ext_v1_reviews_daily_get", extSec, nil, "", stdResponses("200", "#/components/schemas/ReviewBatch", "401", "403", "404", "422", "500", "503")},
		{"GET", "/api/ext/v1/reviews/{batch_id}", "review_batch_api_ext_v1_reviews_batch_id_get", extSec, []openAPIParam{pathParam("batch_id")}, "", stdResponses("200", "#/components/schemas/ReviewBatch", "401", "403", "404", "422", "500", "503")},
		{"POST", "/mcp", "mcp_post", extSec, nil, "object", map[string]any{
			"200": schemaResp("OK", "application/json", map[string]any{"type": "object"}),
			"202": descResp("Notification accepted"), "400": descResp("Invalid protocol request"),
			"401": descResp("Invalid extension credential"), "403": descResp("Origin rejected"),
			"405": descResp("Method not supported"), "406": descResp("Unsupported Accept header"),
			"413": descResp("Request exceeds 64 KiB"), "415": descResp("Unsupported Content-Type"),
		}},
		{"POST", "/hooks/feishu", "hooks_feishu_post", publicSec, nil, "object", map[string]any{
			"200": feishuOK,
			"401": descResp("Invalid platform authentication"), "409": descResp("Message payload changed"),
			"422": descResp("Invalid event"), "500": descResp("Event could not be persisted"),
		}},
		{"POST", "/api/app/v1/session", "client_login", publicSec, []openAPIParam{headerParam("Origin", true)}, "#/components/schemas/ClientLogin", stdResponses("200", "#/components/schemas/ClientSession", "401", "403", "404", "409", "415", "422", "429", "500", "501", "503")},
		{"GET", "/api/app/v1/session", "client_session", appSec, nil, "", stdResponses("200", "#/components/schemas/ClientSession", "401", "403", "404", "409", "415", "422", "429", "500", "503")},
		{"DELETE", "/api/app/v1/session", "client_logout", appSec, []openAPIParam{headerParam("Origin", true), headerParam("X-CSRF-Token", true)}, "", stdResponses("204", "", "401", "403", "404", "409", "415", "422", "429", "500", "503")},
		{"POST", "/api/app/v1/telemetry", "client_telemetry_batch", appSec, []openAPIParam{headerParam("X-CSRF-Token", true)}, "/telemetry/schema-v1.json", map[string]any{
			"204": descResp("Batch processed"), "401": descResp("401"), "403": descResp("403"), "415": descResp("415"), "422": descResp("422"),
		}},
		{"GET", "/api/app/v1/capabilities", "client_capabilities", publicSec, nil, "", stdResponses("200", "#/components/schemas/ClientCapabilities", "401", "403", "404", "409", "415", "422", "429", "500", "503")},
		{"GET", "/api/app/v1/sync/notes", "client_sync_notes", appSec, []openAPIParam{
			queryParam("cursor", "string", ""), queryParam("limit", "integer", "100"),
		}, "", stdResponses("200", "#/components/schemas/SyncNotesResponse", "401", "403", "405", "422", "500", "503")},
		{"GET", "/api/app/v1/notes", "client_notes", appSec, []openAPIParam{
			queryParam("state", "string", "NORMAL"), queryParam("page_size", "integer", "20"), queryParam("page_token", "string", ""),
			queryParam("order", "string", "created_desc"), queryParam("query", "string", ""), queryParam("tag", "string", ""), queryParam("date", "string", ""),
		}, "", stdResponses("200", "#/components/schemas/ClientNotePage", "401", "403", "404", "409", "415", "422", "429", "500", "503")},
		{"POST", "/api/app/v1/notes", "client_create_note", appSec, []openAPIParam{
			headerParam("Origin", true), headerParam("X-CSRF-Token", true), headerParam("Idempotency-Key", true),
		}, "#/components/schemas/ClientCreateNote", noteWriteResponses([]string{"200", "201"}, "#/components/schemas/ClientNote", "401", "403", "404", "409", "415", "422", "429", "500", "503")},
		{"GET", "/api/app/v1/notes/{memo_id}", "client_get_note", appSec, []openAPIParam{pathParam("memo_id")}, "", stdResponses("200", "#/components/schemas/ClientNote", "401", "403", "404", "409", "415", "422", "429", "500", "503")},
		{"PATCH", "/api/app/v1/notes/{memo_id}", "client_update_note", appSec, []openAPIParam{
			pathParam("memo_id"), headerParam("Origin", true), headerParam("X-CSRF-Token", true), headerParam("Idempotency-Key", true),
		}, "#/components/schemas/ClientUpdateNote", stdResponses("200", "#/components/schemas/ClientNote", "401", "403", "404", "409", "415", "422", "429", "500", "503")},
		{"DELETE", "/api/app/v1/notes/{memo_id}", "client_delete_note", appSec, []openAPIParam{
			pathParam("memo_id"), headerParam("Origin", true), headerParam("X-CSRF-Token", true),
		}, "#/components/schemas/ClientDeleteNote", stdResponses("204", "", "401", "403", "404", "409", "415", "422", "429", "500", "503")},
		{"GET", "/api/app/v1/notes/{memo_id}/attachments/{attachment_id}/content", "client_get_attachment", appSec, []openAPIParam{
			pathParam("memo_id"), pathParam("attachment_id"), headerParam("Range", false),
		}, "", map[string]any{
			"200": schemaResp("OK", "application/octet-stream", map[string]any{"type": "string", "format": "binary"}),
			"206": schemaResp("Partial Content", "application/octet-stream", map[string]any{"type": "string", "format": "binary"}),
			"401": jsonResp("401", "#/components/schemas/ErrorResponse"), "403": jsonResp("403", "#/components/schemas/ErrorResponse"),
			"404": jsonResp("404", "#/components/schemas/ErrorResponse"), "409": jsonResp("409", "#/components/schemas/ErrorResponse"),
			"416": descResp("Range Not Satisfiable"),
			"422": jsonResp("422", "#/components/schemas/ErrorResponse"), "503": jsonResp("503", "#/components/schemas/ErrorResponse"),
		}},
		{"HEAD", "/api/app/v1/notes/{memo_id}/attachments/{attachment_id}/content", "client_head_attachment", appSec, []openAPIParam{
			pathParam("memo_id"), pathParam("attachment_id"), headerParam("Range", false),
		}, "", map[string]any{
			"200": descResp("OK"), "206": descResp("Partial Content"),
			"401": jsonResp("401", "#/components/schemas/ErrorResponse"), "403": jsonResp("403", "#/components/schemas/ErrorResponse"),
			"404": jsonResp("404", "#/components/schemas/ErrorResponse"), "409": jsonResp("409", "#/components/schemas/ErrorResponse"),
			"416": descResp("Range Not Satisfiable"),
			"422": jsonResp("422", "#/components/schemas/ErrorResponse"), "503": jsonResp("503", "#/components/schemas/ErrorResponse"),
		}},
		{"GET", "/api/app/v1/tags", "client_tags", appSec, []openAPIParam{queryParam("state", "string", "NORMAL")}, "", stdResponses("200", "#/components/schemas/ClientTags", "401", "403", "404", "409", "415", "422", "429", "500", "503")},
		{"GET", "/api/app/v1/calendar", "client_calendar", appSec, []openAPIParam{queryParam("state", "string", "NORMAL"), {Name: "month", In: "query", Required: true, Schema: map[string]any{"type": "string"}}}, "", stdResponses("200", "#/components/schemas/ClientCalendar", "401", "403", "404", "409", "415", "422", "429", "500", "503")},
		{"GET", "/api/app/v1/admin/notifications", "client_list_notifications", appSec, []openAPIParam{queryParam("kind", "string", ""), queryParam("status", "string", "")}, "", stdResponses("200", "#/components/schemas/NotificationList", "401", "403", "404", "409", "422", "500", "503")},
		{"GET", "/api/app/v1/admin/notifications/{notification_id}", "client_get_notification", appSec, []openAPIParam{pathParam("notification_id")}, "", stdResponses("200", "#/components/schemas/NotificationDetail", "401", "403", "404", "409", "422", "500", "503")},
		{"POST", "/api/app/v1/admin/notifications/{notification_id}/retry", "client_retry_notification", appSec, []openAPIParam{
			pathParam("notification_id"), headerParam("Origin", true), headerParam("X-CSRF-Token", true),
		}, "#/components/schemas/EmptyRequest", stdResponses("200", "#/components/schemas/NotificationDetail", "401", "403", "404", "409", "422", "500", "503")},
		{"GET", "/api/app/v1/settings", "client_get_settings", appSec, nil, "", stdResponses("200", "#/components/schemas/SettingsView", "401", "403", "404", "405", "422", "500", "503")},
		{"PATCH", "/api/app/v1/settings", "client_update_settings", appSec, []openAPIParam{headerParam("Origin", true), headerParam("X-CSRF-Token", true)}, "#/components/schemas/SettingsUpdate", stdResponses("200", "#/components/schemas/SettingsView", "401", "403", "404", "405", "409", "415", "422", "500", "503")},
		{"GET", "/api/app/v1/models", "client_models_status", appSec, nil, "", stdResponses("200", "#/components/schemas/ClientModels", "401", "403", "404", "405", "422", "500", "503")},
		{"POST", "/api/app/v1/models/embedding/check", "client_models_embedding", appSec, []openAPIParam{headerParam("Origin", true), headerParam("X-CSRF-Token", true)}, "#/components/schemas/ClientModelCheckRequest", stdResponses("200", "#/components/schemas/ClientModelCheck", "401", "403", "404", "405", "409", "415", "422", "500", "503")},
		{"POST", "/api/app/v1/models/llm/check", "client_models_llm", appSec, []openAPIParam{headerParam("Origin", true), headerParam("X-CSRF-Token", true)}, "#/components/schemas/ClientModelCheckRequest", stdResponses("200", "#/components/schemas/ClientModelCheck", "401", "403", "404", "405", "409", "415", "422", "500", "503")},
		{"GET", "/api/app/v1/reviews/daily", "client_daily_review", appSec, nil, "", stdResponses("200", "#/components/schemas/ClientReviewBatch", "401", "403", "404", "405", "409", "413", "415", "422", "500", "503")},
		{"POST", "/api/app/v1/walks/start", "client_walk_start", appSec, []openAPIParam{headerParam("X-CSRF-Token", true)}, "#/components/schemas/ClientWalkStart", stdResponses("200", "#/components/schemas/ClientWalkResponse", "401", "403", "404", "405", "409", "413", "415", "422", "500", "503")},
		{"POST", "/api/app/v1/walks/next", "client_walk_next", appSec, []openAPIParam{headerParam("X-CSRF-Token", true)}, "#/components/schemas/ClientWalkNext", stdResponses("200", "#/components/schemas/ClientWalkResponse", "401", "403", "404", "405", "409", "413", "415", "422", "500", "503")},
		{"GET", "/api/app/v1/chats", "client_list_chats", appSec, []openAPIParam{queryParam("page_size", "integer", "20"), queryParam("page_token", "string", "")}, "", chatResponses([]string{"200"}, "application/json", chatJSON("ChatPage"), "401", "403", "404", "405", "409", "413", "415", "422", "500", "503")},
		{"POST", "/api/app/v1/chats", "client_create_chat", appSec, []openAPIParam{headerParam("X-CSRF-Token", true)}, "#/components/schemas/ChatCreate", chatResponses([]string{"200", "201"}, "application/json", chatJSON("ChatSession"), "401", "403", "404", "405", "409", "413", "415", "422", "500", "503")},
		{"GET", "/api/app/v1/chats/{chat_id}", "client_get_chat", appSec, []openAPIParam{pathParam("chat_id")}, "", chatResponses([]string{"200"}, "application/json", chatJSON("ChatSession"), "401", "403", "404", "405", "409", "413", "415", "422", "500", "503")},
		{"PATCH", "/api/app/v1/chats/{chat_id}", "client_update_chat", appSec, []openAPIParam{headerParam("X-CSRF-Token", true), pathParam("chat_id")}, "#/components/schemas/ChatUpdate", chatResponses([]string{"200"}, "application/json", chatJSON("ChatSession"), "401", "403", "404", "405", "409", "413", "415", "422", "500", "503")},
		{"DELETE", "/api/app/v1/chats/{chat_id}", "client_delete_chat", appSec, []openAPIParam{headerParam("X-CSRF-Token", true), pathParam("chat_id")}, "object", chatResponses([]string{"204"}, "", nil, "401", "403", "404", "405", "409", "413", "415", "422", "500", "503")},
		{"GET", "/api/app/v1/chat-materials", "client_select_chat_materials", appSec, []openAPIParam{
			queryParam("tag_match", "string", "all"), queryParam("format", "string", "full"), queryParam("state", "string", "ALL"),
			queryParam("query", "string", ""), queryParam("tag", "string", ""), queryParam("from", "string", ""), queryParam("until", "string", ""),
			queryParam("page_size", "integer", "100"), queryParam("page_token", "string", ""),
		}, "", map[string]any{
			"200": chatMaterialsOK, "401": chatErrRef, "403": chatErrRef, "404": chatErrRef,
			"405": chatErrRef, "409": chatErrRef, "413": chatErrRef, "415": chatErrRef,
			"422": chatErrRef, "500": chatErrRef, "503": chatErrRef,
		}},
		{"POST", "/api/app/v1/chat-materials/resolve", "client_resolve_chat_materials", appSec, []openAPIParam{headerParam("X-CSRF-Token", true)}, "#/components/schemas/ChatNames", chatResponses([]string{"200"}, "application/json", chatJSON("ChatMaterials"), "401", "403", "404", "405", "409", "413", "415", "422", "500", "503")},
		{"POST", "/api/app/v1/chats/{chat_id}/messages", "client_generate_chat", appSec, []openAPIParam{headerParam("X-CSRF-Token", true), pathParam("chat_id")}, "#/components/schemas/ChatGenerate", chatResponses([]string{"200"}, "text/event-stream", map[string]any{"type": "string"}, "401", "403", "404", "405", "409", "413", "415", "422", "500", "503")},
		{"POST", "/api/app/v1/chats/{chat_id}/messages/{request_id}/cancel", "client_cancel_chat", appSec, []openAPIParam{headerParam("X-CSRF-Token", true), pathParam("chat_id"), pathParam("request_id")}, "object", chatResponses([]string{"200"}, "application/json", chatJSON("ChatTurn"), "401", "403", "404", "405", "409", "413", "415", "422", "500", "503")},
		{"POST", "/api/app/v1/chats/{chat_id}/save-selection", "client_save_chat_selection", appSec, []openAPIParam{headerParam("X-CSRF-Token", true), pathParam("chat_id")}, "#/components/schemas/ChatSaveSelection", chatResponses([]string{"200", "201"}, "application/json", chatJSON("ClientNote"), "default")},
		{"GET", "/api/app/v1/chats/{chat_id}/save-selection/{operation_key}", "client_chat_save_receipt", appSec, []openAPIParam{pathParam("chat_id"), pathParam("operation_key")}, "", chatResponses([]string{"200"}, "application/json", chatJSON("ChatSaveReceipt"), "default")},
		{"POST", "/api/app/v1/chats/{chat_id}/messages/{request_id}/save", "client_save_chat", appSec, []openAPIParam{headerParam("X-CSRF-Token", true), pathParam("chat_id"), pathParam("request_id")}, "#/components/schemas/ChatVersion", chatResponses([]string{"200", "201"}, "application/json", chatJSON("ClientNote"), "401", "403", "404", "405", "409", "413", "415", "422", "500", "503")},
		{"GET", "/api/app/v1/map", "client_cognitive_map", appSec, nil, "", stdResponses("200", "#/components/schemas/MapResponse", "401", "403", "404", "405", "422", "500", "503")},
		{"POST", "/api/app/v1/search", "client_search", appSec, []openAPIParam{headerParam("X-CSRF-Token", true)}, "#/components/schemas/ClientSearchRequest", stdResponses("200", "#/components/schemas/ClientSearchPage", "401", "403", "405", "409", "413", "422", "503")},
		{"POST", "/api/app/v1/map/rebuild", "client_rebuild_star_map", appSec, []openAPIParam{headerParam("Origin", true), headerParam("X-CSRF-Token", true)}, "", map[string]any{
			"202": mapRebuildOK,
			"401": jsonResp("401", "#/components/schemas/ErrorResponse"), "403": jsonResp("403", "#/components/schemas/ErrorResponse"),
			"405": jsonResp("405", "#/components/schemas/ErrorResponse"), "409": jsonResp("409", "#/components/schemas/ErrorResponse"),
			"422": jsonResp("422", "#/components/schemas/ErrorResponse"), "503": jsonResp("503", "#/components/schemas/ErrorResponse"),
		}},
	}

	paths := map[string]map[string]any{}
	for _, op := range ops {
		method := map[string]any{
			"operationId": op.OperationID,
		}
		if op.Security != nil {
			method["security"] = op.Security
		}
		if len(op.Parameters) > 0 {
			params := make([]map[string]any, 0, len(op.Parameters))
			for _, p := range op.Parameters {
				item := map[string]any{"name": p.Name, "in": p.In, "required": p.Required, "schema": p.Schema}
				if p.Description != "" {
					item["description"] = p.Description
				}
				params = append(params, item)
			}
			method["parameters"] = params
		}
		if op.RequestRef != "" {
			var schema map[string]any
			if op.RequestRef == "object" {
				schema = map[string]any{"type": "object"}
			} else {
				schema = map[string]any{"$ref": op.RequestRef}
			}
			method["requestBody"] = map[string]any{
				"required": true,
				"content":  map[string]any{"application/json": map[string]any{"schema": schema}},
			}
		}
		method["responses"] = op.Responses
		if paths[op.Path] == nil {
			paths[op.Path] = map[string]any{}
		}
		paths[op.Path][lowerASCII(op.Method)] = method
	}

	doc := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":   "Moerise API",
			"version": Version,
		},
		"paths": paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"HTTPBearer": map[string]any{
					"type":   "http",
					"scheme": "bearer",
				},
				"BrowserSession": map[string]any{
					"type": "apiKey",
					"in":   "cookie",
					"name": "__Host-moerise_session",
				},
				"LocalBrowserSession": map[string]any{
					"type": "apiKey",
					"in":   "cookie",
					"name": "moerise_local_session",
				},
			},
			"responses": map[string]any{
				"ChatError": map[string]any{
					"description": "客户端聊天错误。流开始后的错误使用 ChatStreamError，EOF 不代表成功。",
					"content": map[string]any{
						"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/ErrorResponse"}},
					},
					"headers": map[string]any{
						"Cache-Control": map[string]any{"schema": map[string]any{"type": "string", "const": "no-store"}},
						"X-Request-ID":  map[string]any{"schema": map[string]any{"type": "string"}},
					},
				},
			},
			"schemas": buildOpenAPISchemas(),
		},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return data
}

func lowerASCII(value string) string {
	b := []byte(value)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
