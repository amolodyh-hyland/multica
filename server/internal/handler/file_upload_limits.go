package handler

import (
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/foldersnapshot"
)

const maxUploadSize = 100 << 20 // 100 MiB per file

// File contents have their own limit; multipart boundaries and metadata
// need extra room within the whole-request cap.
const maxUploadBodySize = foldersnapshot.MaxArchiveBytes + (1 << 20)

func parseUploadForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBodySize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		return err
	}
	for _, header := range r.MultipartForm.File["file"] {
		limit := int64(maxUploadSize)
		if strings.HasSuffix(header.Filename, foldersnapshot.Extension) {
			limit = foldersnapshot.MaxArchiveBytes
		}
		if header.Size > limit {
			return &http.MaxBytesError{Limit: limit}
		}
	}
	return nil
}
