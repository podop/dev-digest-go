// Package domain holds the rules of the GitLab connections context (spec FR1, FR2, FR4, FR11): the
// stored definition of a connection, the validation of its base URL, name and token, and the
// evaluation of a token test. It imports the standard library and llm/custom (the address rules
// shared with the custom model providers) only.
package domain

import (
	"errors"
	"time"
)

// Limit is the most connections an installation can have (spec FR1).
const Limit = 20

// Errors a Store returns; the use cases map them to application errors.
var (
	// ErrNotFound means no connection has that id (spec FR3: 404 gitlab_connection_not_found).
	ErrNotFound = errors.New("gitlab connection not found")
	// ErrExists means a connection for that host key exists already (409 gitlab_connection_exists).
	ErrExists = errors.New("gitlab connection already exists")
	// ErrLimit means Limit connections exist already (422 gitlab_connection_limit).
	ErrLimit = errors.New("gitlab connection limit reached")
)

// Connection is the stored definition of one GitLab connection. The token is not part of it: it
// lives in the secrets store under the connection id (spec FR4).
type Connection struct {
	// ID is the server-generated UUID, immutable.
	ID string
	// Name is the display name, 1-80 characters (CheckName).
	Name string
	// BaseURL is the normalized instance URL (CheckBaseURL): https, no trailing slash, no :443.
	BaseURL string
	// Host is the host key derived from BaseURL: the lower-cased host name, plus ":<port>" when the
	// port is not 443. Repositories bind to a connection by it.
	Host      string
	CreatedAt time.Time
	UpdatedAt time.Time
}
