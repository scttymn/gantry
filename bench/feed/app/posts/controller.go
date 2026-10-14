// Package posts is the feed's API: the newest posts, one post, writing one
// and liking one.
package posts

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"feed/app/models"
	"feed/app/shared/bearer"
)

// Controller serves posts.
type Controller struct {
	DB *db.DB
}

var (
	errInvalidID = web.Status(http.StatusBadRequest, errors.New("invalid post id"))
	errNotFound  = web.Status(http.StatusNotFound, errors.New("post not found"))
	errMalformed = web.Status(http.StatusBadRequest, errors.New("malformed JSON body"))
	errRequired  = web.Status(http.StatusBadRequest, errors.New("body is required"))
	errTooLong   = web.Status(http.StatusBadRequest, errors.New("body must be at most 500 characters"))
)

// Index is GET /feed: the 20 newest posts.
func (c Controller) Index(w http.ResponseWriter, r *http.Request) error {
	posts, err := models.New(c.DB.Read).Feed(r.Context())
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, struct {
		Posts []models.FeedRow `json:"posts"`
	}{posts})
}

// Show is GET /posts/{id}.
func (c Controller) Show(w http.ResponseWriter, r *http.Request) error {
	id, ok := bearer.ID(r.PathValue("id"))
	if !ok {
		return errInvalidID
	}
	post, err := models.New(c.DB.Read).GetPost(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, struct {
		Post models.GetPostRow `json:"post"`
	}{post})
}

// Create is POST /posts, from its token's user.
func (c Controller) Create(w http.ResponseWriter, r *http.Request) error {
	user, _ := web.Get(r, bearer.Current)
	var in struct {
		Body any `json:"body"`
	}
	if err := web.ReadJSON(r, &in); err != nil {
		return errMalformed
	}
	body, _ := in.Body.(string)
	body = strings.TrimSpace(body)
	if body == "" {
		return errRequired
	}
	if utf8.RuneCountInString(body) > 500 {
		return errTooLong
	}
	post := models.GetPostRow{Body: body, Author: user.Name}
	err := c.DB.Tx(r.Context(), func(tx *db.Tx) error {
		row, err := models.New(tx).CreatePost(r.Context(), models.CreatePostParams{UserID: user.ID, Body: body})
		post.ID, post.CreatedAt = row.ID, row.CreatedAt
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusCreated, struct {
		Post models.GetPostRow `json:"post"`
	}{post})
}

// Like is POST /posts/{id}/like: 201 the first time, 200 after.
func (c Controller) Like(w http.ResponseWriter, r *http.Request) error {
	user, _ := web.Get(r, bearer.Current)
	id, ok := bearer.ID(r.PathValue("id"))
	if !ok {
		return errInvalidID
	}
	var liked int64
	err := c.DB.Tx(r.Context(), func(tx *db.Tx) (err error) {
		liked, err = models.New(tx).LikePost(r.Context(), models.LikePostParams{UserID: user.ID, PostID: id})
		return err
	})
	// The foreign key says the post isn't there: gantry turns them on.
	if db.IsForeignKey(err) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	status := http.StatusOK
	if liked == 1 {
		status = http.StatusCreated
	}
	return web.JSON(w, status, struct {
		Liked   bool  `json:"liked"`
		Already bool  `json:"already_liked"`
		PostID  int64 `json:"post_id"`
	}{true, liked == 0, id})
}
