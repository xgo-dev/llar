package artifact

import (
	"context"
	"errors"
)

var ErrNotFound = errors.New("artifact not found")

type Key struct {
	Module    string
	Version   string
	MatrixStr string
}

type Source struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type Artifact struct {
	Source   Source `json:"source"`
	Type     string `json:"type"`
	Metadata string `json:"metadata"`
	Checksum string `json:"checksum"`
}

type Store interface {
	Get(ctx context.Context, key Key) (Artifact, error)
	Put(ctx context.Context, key Key, artifact Artifact) (Artifact, error)
	Delete(ctx context.Context, key Key) error
}
