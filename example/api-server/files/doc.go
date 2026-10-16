// Package files implements the api-server example's file routes: upload,
// list, get, a presigned download link valid for 15 minutes, and delete,
// all under /api/files and all behind the auth service's middleware. Files
// are stored through storage.Provider and download links signed by
// storage.Presigner, their metadata in the database with this package's
// migrations, and per-user lookups are cached.
//
// A successful upload emits FileUploaded on the event bus; nothing in the
// example subscribes to it.
package files
