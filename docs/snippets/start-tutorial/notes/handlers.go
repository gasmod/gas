// #region types

package notes

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gasmod/gas"
)

// Note is a note as the API returns it.
type Note struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type createRequest struct {
	Title string `json:"title" validate:"required,max=200"`
	Body  string `json:"body"  validate:"max=10000"`
}

// #endregion types

// #region create
// create stores a note owned by the caller. The owner is the subject of the
// API key the request authenticated with.
func (s *Service) create(ctx gas.Context) error {
	owner, err := ctx.Principal()
	if err != nil {
		return err
	}

	var req createRequest
	if err := ctx.BindJSON(&req); err != nil {
		return err
	}

	note := Note{Title: req.Title, Body: req.Body, CreatedAt: time.Now().UTC()}
	err = s.db.DB().QueryRowContext(ctx,
		`INSERT INTO notes (owner, title, body, created_at) VALUES (?, ?, ?, ?) RETURNING id`,
		owner.Subject(), note.Title, note.Body, note.CreatedAt,
	).Scan(&note.ID)
	if err != nil {
		return fmt.Errorf("inserting note: %w", err)
	}

	return ctx.JSON(http.StatusCreated, note)
}

// #endregion create

// #region list
// list returns the caller's notes, oldest first.
func (s *Service) list(ctx gas.Context) (err error) {
	owner, err := ctx.Principal()
	if err != nil {
		return err
	}

	rows, err := s.db.DB().QueryContext(ctx,
		`SELECT id, title, body, created_at FROM notes WHERE owner = ? ORDER BY id`,
		owner.Subject(),
	)
	if err != nil {
		return fmt.Errorf("listing notes: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	notes := []Note{}
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.Title, &n.Body, &n.CreatedAt); err != nil {
			return fmt.Errorf("scanning note: %w", err)
		}
		notes = append(notes, n)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("listing notes: %w", err)
	}

	return ctx.JSON(http.StatusOK, notes)
}

// #endregion list

// #region get
// get returns one of the caller's notes. Another owner's note is a 404, the
// same as a note that does not exist.
func (s *Service) get(ctx gas.Context) error {
	owner, err := ctx.Principal()
	if err != nil {
		return err
	}
	id, err := strconv.ParseInt(ctx.Param("id"), 10, 64)
	if err != nil {
		return gas.BadRequest("id must be an integer")
	}

	var n Note
	err = s.db.DB().QueryRowContext(ctx,
		`SELECT id, title, body, created_at FROM notes WHERE id = ? AND owner = ?`,
		id, owner.Subject(),
	).Scan(&n.ID, &n.Title, &n.Body, &n.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return gas.NotFound("note not found")
	}
	if err != nil {
		return fmt.Errorf("reading note: %w", err)
	}

	return ctx.JSON(http.StatusOK, n)
}

// #endregion get

// #region delete
// delete removes one of the caller's notes.
func (s *Service) delete(ctx gas.Context) error {
	owner, err := ctx.Principal()
	if err != nil {
		return err
	}
	id, err := strconv.ParseInt(ctx.Param("id"), 10, 64)
	if err != nil {
		return gas.BadRequest("id must be an integer")
	}

	res, err := s.db.DB().ExecContext(ctx,
		`DELETE FROM notes WHERE id = ? AND owner = ?`,
		id, owner.Subject(),
	)
	if err != nil {
		return fmt.Errorf("deleting note: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("deleting note: %w", err)
	}
	if n == 0 {
		return gas.NotFound("note not found")
	}

	return ctx.NoContent()
}

// #endregion delete
