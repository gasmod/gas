package files

import (
	"embed"
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

// FileUploaded is emitted after a file is successfully stored. Other services
// can subscribe to react to new uploads.
var FileUploaded = gas.Event[FileUploadedPayload]{Name: "file:uploaded"}

// FileUploadedPayload carries the identity of a newly stored file.
type FileUploadedPayload struct {
	Name   string
	FileID uuid.UUID
	UserID uuid.UUID
}

// Service exposes the authenticated file CRUD routes, storing blobs in the
// StorageProvider and metadata in the database, with a cache in front of reads.
type Service struct {
	router  *gas.Router
	bus     *gas.EventBus
	db      gas.DatabaseProvider
	mgr     gas.MigrationManager
	storage gas.StorageProvider
	cache   gas.CacheProvider
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
	authSvc *auth.Service,
) *Service {
	return &Service{
		router:  router,
		bus:     bus,
		db:      dbProvider,
		mgr:     mgr,
		storage: storage,
		cache:   cache,
		auth:    authSvc,
		queries: db.New(dbProvider.DB()),
	}
}

// Name identifies the service for route, middleware, and migration ownership.
func (s *Service) Name() string { return "files" }

// Init registers migrations and the authenticated /api/files route group.
func (s *Service) Init() error {
	if err := s.mgr.RegisterFS(s.Name(), migrationsFS); err != nil {
		return fmt.Errorf("registering files migrations: %w", err)
	}

	// All file routes require authentication.
	s.router.Group(func(sub *gas.Router) {
		sub.UseMiddlewareFunc(s.auth.Middleware())

		sub.Handle(s.Name(), http.MethodPost, "/api/files", s.handleUpload)
		sub.Handle(s.Name(), http.MethodGet, "/api/files", s.handleList)
		sub.Handle(s.Name(), http.MethodGet, "/api/files/{id}", s.handleGet)
		sub.Handle(s.Name(), http.MethodGet, "/api/files/{id}/download", s.handleDownload)
		sub.Handle(s.Name(), http.MethodDelete, "/api/files/{id}", s.handleDelete)
	})

	return nil
}

// Close releases service resources at shutdown. This service holds none.
func (s *Service) Close() error { return nil }

// --- Response types ---

type fileResponse struct {
	CreatedAt   time.Time `json:"created_at"`
	Name        string    `json:"name"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	ID          uuid.UUID `json:"id"`
}

type downloadResponse struct {
	URL string `json:"url"`
}

// --- Handlers ---

func (s *Service) handleUpload(ctx gas.Context) error {
	// Parse multipart form — 32MB max memory.
	r := ctx.Request()
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return &apiError{Status: http.StatusBadRequest, Message: "invalid multipart form"}
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		return &apiError{Status: http.StatusBadRequest, Message: "missing file field"}
	}
	defer func() { _ = file.Close() }()

	principal := gas.PrincipalFromContext(ctx)
	userID, _ := uuid.Parse(principal.Subject())

	// Generate a unique storage key.
	storageKey := fmt.Sprintf("%s/%s/%s", userID, uuid.New(), header.Filename)

	// Detect content type from the multipart header.
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Upload to S3 with explicit content type so downloads serve the
	// correct MIME type without needing to guess from the key.
	if uploadErr := s.storage.Upload(ctx, storageKey, file, gas.WithContentType(contentType)); uploadErr != nil {
		return fmt.Errorf("upload to storage: %w", uploadErr)
	}

	f, err := s.queries.CreateFile(ctx, db.CreateFileParams{
		UserID:      userID,
		Name:        header.Filename,
		Size:        header.Size,
		ContentType: contentType,
		StorageKey:  storageKey,
	})
	if err != nil {
		return fmt.Errorf("create file record: %w", err)
	}

	// Emit event for other services to react to.
	gas.Emit(s.bus, FileUploaded, FileUploadedPayload{
		FileID: f.ID,
		UserID: userID,
		Name:   f.Name,
	})

	return ctx.JSON(http.StatusCreated, fileResponse{
		ID:          f.ID,
		Name:        f.Name,
		Size:        f.Size,
		ContentType: f.ContentType,
		CreatedAt:   f.CreatedAt,
	})
}

func (s *Service) handleList(ctx gas.Context) error {
	principal := gas.PrincipalFromContext(ctx)
	userID, _ := uuid.Parse(principal.Subject())

	files, err := s.queries.ListFilesByUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("listing files: %w", err)
	}

	resp := make([]fileResponse, len(files))
	for i, f := range files {
		resp[i] = fileResponse{
			ID:          f.ID,
			Name:        f.Name,
			Size:        f.Size,
			ContentType: f.ContentType,
			CreatedAt:   f.CreatedAt,
		}
	}

	return ctx.JSON(http.StatusOK, resp)
}

func (s *Service) handleGet(ctx gas.Context) error {
	fileID, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		return &apiError{Status: http.StatusBadRequest, Message: "invalid file id"}
	}

	principal := gas.PrincipalFromContext(ctx)
	userID, _ := uuid.Parse(principal.Subject())

	// Check cache first. The key is scoped to the requesting user: the
	// ownership check below is unreachable on a cache hit, so an unscoped key
	// would serve one user's file to any other authenticated caller.
	cacheKey := userCacheKey("file", userID, fileID)
	if data, cacheErr := s.cache.Get(ctx, cacheKey); cacheErr == nil {
		var resp fileResponse
		if json.Unmarshal(data, &resp) == nil {
			return ctx.JSON(http.StatusOK, resp)
		}
	}

	f, err := s.queries.GetFileByID(ctx, fileID)
	if err != nil {
		return &apiError{Status: http.StatusNotFound, Message: "file not found"}
	}

	// Verify ownership.
	if f.UserID != userID {
		return &apiError{Status: http.StatusNotFound, Message: "file not found"}
	}

	resp := fileResponse{
		ID:          f.ID,
		Name:        f.Name,
		Size:        f.Size,
		ContentType: f.ContentType,
		CreatedAt:   f.CreatedAt,
	}

	// Cache for 5 minutes.
	// Best-effort: a failed cache write must not fail the request.
	if data, marshalErr := json.Marshal(resp); marshalErr == nil {
		_ = s.cache.Set(ctx, cacheKey, data, 5*time.Minute)
	}

	return ctx.JSON(http.StatusOK, resp)
}

func (s *Service) handleDownload(ctx gas.Context) error {
	fileID, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		return &apiError{Status: http.StatusBadRequest, Message: "invalid file id"}
	}

	principal := gas.PrincipalFromContext(ctx)
	userID, _ := uuid.Parse(principal.Subject())

	// Check cache for presigned URL. Scoped to the requesting user for the same
	// reason as handleGet, and more sharply here: an unscoped hit hands a
	// working presigned URL for someone else's object to any authenticated user.
	cacheKey := userCacheKey("download", userID, fileID)
	if data, cacheErr := s.cache.Get(ctx, cacheKey); cacheErr == nil {
		return ctx.JSON(http.StatusOK, downloadResponse{URL: string(data)})
	}

	f, err := s.queries.GetFileByID(ctx, fileID)
	if err != nil {
		return &apiError{Status: http.StatusNotFound, Message: "file not found"}
	}

	// Verify ownership.
	if f.UserID != userID {
		return &apiError{Status: http.StatusNotFound, Message: "file not found"}
	}

	// Generate presigned URL valid for 15 minutes.
	url, err := s.storage.PresignDownloadURL(ctx, f.StorageKey, 15*time.Minute)
	if err != nil {
		return fmt.Errorf("presign url: %w", err)
	}

	// Cache the presigned URL for 14 minutes (slightly less than expiry).
	// Best-effort: a failed cache write must not fail the request.
	_ = s.cache.Set(ctx, cacheKey, []byte(url), 14*time.Minute)

	return ctx.JSON(http.StatusOK, downloadResponse{URL: url})
}

func (s *Service) handleDelete(ctx gas.Context) error {
	fileID, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		return &apiError{Status: http.StatusBadRequest, Message: "invalid file id"}
	}

	f, err := s.queries.GetFileByID(ctx, fileID)
	if err != nil {
		return &apiError{Status: http.StatusNotFound, Message: "file not found"}
	}

	principal := gas.PrincipalFromContext(ctx)
	userID, _ := uuid.Parse(principal.Subject())
	if f.UserID != userID {
		return &apiError{Status: http.StatusNotFound, Message: "file not found"}
	}

	// Delete from S3 first, then DB.
	if err := s.storage.Delete(ctx, f.StorageKey); err != nil {
		return fmt.Errorf("delete from storage: %w", err)
	}

	if err := s.queries.DeleteFile(ctx, db.DeleteFileParams{
		ID:     fileID,
		UserID: userID,
	}); err != nil {
		return fmt.Errorf("delete file record: %w", err)
	}

	// Invalidate cache.
	// Best-effort: a failed eviction must not fail an otherwise complete delete.
	_ = s.cache.Delete(ctx, userCacheKey("file", userID, fileID))
	_ = s.cache.Delete(ctx, userCacheKey("download", userID, fileID))

	if err := ctx.NoContent(); err != nil {
		return fmt.Errorf("writing response: %w", err)
	}
	return nil
}

// userCacheKey scopes a cache entry to one user, so a cached response can only
// ever be served back to the user it was built for.
func userCacheKey(prefix string, userID, fileID uuid.UUID) string {
	return fmt.Sprintf("%s:%s:%s", prefix, userID, fileID)
}

type apiError struct {
	Message string `json:"error"`
	Status  int    `json:"-"`
}

func (e *apiError) Error() string   { return e.Message }
func (e *apiError) StatusCode() int { return e.Status }
