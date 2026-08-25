package shares

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/example/api-server/auth"
	"github.com/gasmod/gas/example/api-server/db"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

//go:embed templates/*
var templatesFS embed.FS

// Service handles creating share links and sending email notifications
// asynchronously via the job queue.
type Service struct {
	router  *gas.Router
	bus     *gas.EventBus
	db      gas.DatabaseProvider
	mgr     gas.MigrationManager
	storage gas.StorageProvider
	cache   gas.CacheProvider
	queue   gas.JobQueueProvider
	email   gas.EmailProvider
	tmpl    gas.TemplateProvider
	cfg     gas.ConfigProvider
	auth    *auth.Service
	queries *db.Queries
}

// New is the DI constructor; the container injects every parameter.
func New(
	router *gas.Router,
	bus *gas.EventBus,
	dbProvider gas.DatabaseProvider,
	mgr gas.MigrationManager,
	storage gas.StorageProvider,
	cache gas.CacheProvider,
	queue gas.JobQueueProvider,
	email gas.EmailProvider,
	tmpl gas.TemplateProvider,
	cfg gas.ConfigProvider,
	authSvc *auth.Service,
) *Service {
	return &Service{
		router:  router,
		bus:     bus,
		db:      dbProvider,
		mgr:     mgr,
		storage: storage,
		cache:   cache,
		queue:   queue,
		email:   email,
		tmpl:    tmpl,
		cfg:     cfg,
		auth:    authSvc,
		queries: db.New(dbProvider.DB()),
	}
}

// Name identifies the service for route, middleware, and migration ownership.
func (s *Service) Name() string { return "shares" }

// Init registers migrations and the notification template, then the
// authenticated share-creation route and the public share-lookup route.
func (s *Service) Init() error {
	if err := s.mgr.RegisterFS(s.Name(), migrationsFS); err != nil {
		return fmt.Errorf("registering shares migrations: %w", err)
	}

	// Register email templates into the shared TemplateProvider.
	if err := s.tmpl.RegisterFS(context.Background(), templatesFS); err != nil {
		return fmt.Errorf("registering share templates: %w", err)
	}

	// Protected routes — require authentication.
	s.router.Group(func(sub *gas.Router) {
		sub.UseMiddlewareFunc(s.auth.Middleware())
		sub.Handle(s.Name(), http.MethodPost, "/api/files/{id}/share", s.handleCreateShare)
	})

	// Public route — access shared file by token.
	s.router.Handle(s.Name(), http.MethodGet, "/api/shares/{token}", s.handleGetShare)

	return nil
}

// Close releases service resources at shutdown. This service holds none.
func (s *Service) Close() error { return nil }

// --- Request/Response types ---

type createShareRequest struct {
	RecipientEmail string `json:"recipient_email" validate:"required,email"`
	ExpiresInHours int    `json:"expires_in_hours" validate:"omitempty,min=1,max=720"`
}

type shareResponse struct {
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	Token          string     `json:"token"`
	RecipientEmail string     `json:"recipient_email"`
	ID             uuid.UUID  `json:"id"`
	FileID         uuid.UUID  `json:"file_id"`
}

type publicShareResponse struct {
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	DownloadURL string `json:"download_url"`
	FileSize    int64  `json:"file_size"`
}

// shareEmailJob is the payload enqueued for async email sending.
type shareEmailJob struct {
	RecipientEmail string `json:"recipient_email"`
	SenderEmail    string `json:"sender_email"`
	FileName       string `json:"file_name"`
	ShareToken     string `json:"share_token"`
	ExpiresAt      string `json:"expires_at"`
}

// --- Handlers ---

func (s *Service) handleCreateShare(ctx gas.Context) error {
	fileID, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		return &apiError{Status: http.StatusBadRequest, Message: "invalid file id"}
	}

	var req createShareRequest
	if bindErr := ctx.BindJSON(&req); bindErr != nil {
		return &apiError{Status: http.StatusBadRequest, Message: "invalid request: " + bindErr.Error()}
	}

	// Verify the file exists and belongs to the user.
	f, err := s.queries.GetFileByID(ctx, fileID)
	if err != nil {
		return &apiError{Status: http.StatusNotFound, Message: "file not found"}
	}

	principal := gas.PrincipalFromContext(ctx)
	userID, _ := uuid.Parse(principal.Subject())
	if f.UserID != userID {
		return &apiError{Status: http.StatusNotFound, Message: "file not found"}
	}

	// Generate a unique share token.
	token, err := generateToken(32)
	if err != nil {
		return fmt.Errorf("generate share token: %w", err)
	}

	// Calculate expiry.
	var expiresAt sql.NullTime
	expiresStr := "never"
	if req.ExpiresInHours > 0 {
		t := time.Now().Add(time.Duration(req.ExpiresInHours) * time.Hour)
		expiresAt = sql.NullTime{Time: t, Valid: true}
		expiresStr = t.Format(time.RFC3339)
	}

	share, err := s.queries.CreateShare(ctx, db.CreateShareParams{
		FileID:         fileID,
		UserID:         userID,
		Token:          token,
		RecipientEmail: req.RecipientEmail,
		ExpiresAt:      expiresAt,
	})
	if err != nil {
		return fmt.Errorf("create share: %w", err)
	}

	// Get sender email for the notification.
	sender, err := s.queries.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("get sender: %w", err)
	}

	// Enqueue email notification asynchronously via the job queue.
	job := shareEmailJob{
		RecipientEmail: req.RecipientEmail,
		SenderEmail:    sender.Email,
		FileName:       f.Name,
		ShareToken:     token,
		ExpiresAt:      expiresStr,
	}

	payload, _ := json.Marshal(job)

	// Read queue URL from config. In production this would be a real SQS URL.
	var queueCfg struct {
		ShareNotificationQueue string `json:"share_notification_queue" validate:"required"`
	}
	if err := s.cfg.Bind(&queueCfg); err == nil && queueCfg.ShareNotificationQueue != "" {
		if err := s.queue.Enqueue(ctx, queueCfg.ShareNotificationQueue, payload); err != nil {
			// Log but don't fail the share creation if email enqueue fails.
			// The share link is still valid.
			_ = err
		}
	}

	var resp shareResponse
	resp.ID = share.ID
	resp.FileID = share.FileID
	resp.Token = share.Token
	resp.RecipientEmail = share.RecipientEmail
	resp.CreatedAt = share.CreatedAt
	if share.ExpiresAt.Valid {
		resp.ExpiresAt = &share.ExpiresAt.Time
	}

	return ctx.JSON(http.StatusCreated, resp)
}

func (s *Service) handleGetShare(ctx gas.Context) error {
	token := ctx.Param("token")

	// Check cache first.
	cacheKey := "share:" + token
	if data, err := s.cache.Get(ctx, cacheKey); err == nil {
		var resp publicShareResponse
		if json.Unmarshal(data, &resp) == nil {
			return ctx.JSON(http.StatusOK, resp)
		}
	}

	share, err := s.queries.GetShareByToken(ctx, token)
	if err != nil {
		return &apiError{Status: http.StatusNotFound, Message: "share not found"}
	}

	// Check expiry.
	if share.ExpiresAt.Valid && time.Now().After(share.ExpiresAt.Time) {
		return &apiError{Status: http.StatusGone, Message: "share link has expired"}
	}

	// Generate presigned download URL.
	url, err := s.storage.PresignDownloadURL(ctx, share.FileStorageKey, 15*time.Minute)
	if err != nil {
		return fmt.Errorf("presign url: %w", err)
	}

	resp := publicShareResponse{
		FileName:    share.FileName,
		FileSize:    share.FileSize,
		ContentType: share.FileContentType,
		DownloadURL: url,
	}

	// Cache for 14 minutes (slightly less than presigned URL expiry).
	// Best-effort: a failed cache write must not fail the request.
	if data, marshalErr := json.Marshal(resp); marshalErr == nil {
		_ = s.cache.Set(ctx, cacheKey, data, 14*time.Minute)
	}

	return ctx.JSON(http.StatusOK, resp)
}

// ProcessShareEmail processes a share email job from the queue. This would
// be called by a queue consumer (worker or background goroutine).
func (s *Service) ProcessShareEmail(ctx context.Context, payload []byte) error {
	var job shareEmailJob
	if err := json.Unmarshal(payload, &job); err != nil {
		return fmt.Errorf("unmarshal job: %w", err)
	}

	if err := s.email.SendFromTemplate(ctx, &gas.TemplatedEmail{
		HTMLTemplate: "templates/share_notification.html",
		Data: map[string]string{
			"SenderEmail": job.SenderEmail,
			"FileName":    job.FileName,
			"ShareURL":    "/api/shares/" + job.ShareToken,
			"ExpiresAt":   job.ExpiresAt,
		},
		Email: gas.Email{
			To:      []string{job.RecipientEmail},
			Subject: fmt.Sprintf("%s shared a file with you", job.SenderEmail),
		},
	}); err != nil {
		return fmt.Errorf("sending share notification: %w", err)
	}
	return nil
}

func generateToken(length int) (string, error) {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating share token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

type apiError struct {
	Message string `json:"error"`
	Status  int    `json:"-"`
}

func (e *apiError) Error() string   { return e.Message }
func (e *apiError) StatusCode() int { return e.Status }
