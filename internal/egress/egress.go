package egress

import (
	"time"

	"prism/internal/canon"
	"prism/internal/routing"
)

type ResponseHeader struct {
	ID        string
	Model     canon.ModelID
	CreatedAt time.Time
}

type Egress interface {
	Begin(h ResponseHeader) error
	Frame(ev canon.Event) error
	Lifecycle() routing.ResponseLifecycle
	Flush() error
}
