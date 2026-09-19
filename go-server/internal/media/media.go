package media

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/mase/server/internal/db"
)

var mediaDir string

// Init sets the directory where media files are stored.
func Init(dir string) error {
	mediaDir = dir
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create media dir: %w", err)
	}
	log.Printf("[MEDIA] serving from %s", dir)
	return nil
}

// Handler returns an http.Handler for /upload and /media/{id}.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/upload", handleUpload)
	mux.HandleFunc("/media/", handleServe)
	return mux
}

// handleUpload accepts multipart or raw body uploads.
// POST /upload — requires Bearer token auth.
func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		corsHeaders(w)
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	corsHeaders(w)

	token := bearerToken(r)
	if _, err := db.GetUserIDByToken(token); err != nil {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Limit upload to 100 MB
	r.Body = http.MaxBytesReader(w, r.Body, 100<<20)

	// Detect content type to choose extension
	contentType := r.Header.Get("Content-Type")
	ext := extensionFor(contentType)

	var data []byte
	var err error

	if strings.Contains(contentType, "multipart/form-data") {
		if err = r.ParseMultipartForm(100 << 20); err != nil {
			jsonErr(w, http.StatusBadRequest, "parse_error")
			return
		}
		file, _, ferr := r.FormFile("file")
		if ferr != nil {
			jsonErr(w, http.StatusBadRequest, "no_file")
			return
		}
		defer file.Close()
		data, err = io.ReadAll(file)
	} else {
		data, err = io.ReadAll(r.Body)
	}

	if err != nil || len(data) == 0 {
		jsonErr(w, http.StatusBadRequest, "empty_body")
		return
	}

	filename := randomHex(16) + ext
	path := filepath.Join(mediaDir, filename)
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("[MEDIA] write failed: %v", err)
		jsonErr(w, http.StatusInternalServerError, "write_failed")
		return
	}

	log.Printf("[MEDIA] uploaded %s (%d bytes)", filename, len(data))
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"mediaId":%q}`, filename)
}

// handleServe serves a media file.
// GET /media/{id} — requires Bearer token.
func handleServe(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	token := bearerToken(r)
	if _, err := db.GetUserIDByToken(token); err != nil {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	filename := strings.TrimPrefix(r.URL.Path, "/media/")
	// Reject path traversal
	if filename == "" || strings.Contains(filename, "/") || strings.Contains(filename, "..") {
		jsonErr(w, http.StatusBadRequest, "bad_path")
		return
	}

	path := filepath.Join(mediaDir, filename)
	http.ServeFile(w, r, path)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	// Also support query param for inline image loading
	return r.URL.Query().Get("token")
}

func extensionFor(contentType string) string {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "image/jpeg"):
		return ".jpg"
	case strings.Contains(ct, "image/png"):
		return ".png"
	case strings.Contains(ct, "image/gif"):
		return ".gif"
	case strings.Contains(ct, "image/webp"):
		return ".webp"
	case strings.Contains(ct, "audio/ogg"), strings.Contains(ct, "audio/opus"):
		return ".ogg"
	case strings.Contains(ct, "audio/mp4"), strings.Contains(ct, "audio/aac"):
		return ".m4a"
	case strings.Contains(ct, "video/mp4"):
		return ".mp4"
	case strings.Contains(ct, "application/pdf"):
		return ".pdf"
	default:
		return ".bin"
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b) //nolint:errcheck
	return hex.EncodeToString(b)
}

func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}
