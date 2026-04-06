package main

import (
	"crypto/subtle"
	"embed"
	"html/template"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/amerenda/photos/internal/auth"
	"github.com/amerenda/photos/internal/gallery"
)

//go:embed templates static
var assets embed.FS

var tmpl *template.Template

type server struct {
	publicDir string
	secretDir string
}

func main() {
	photosDir := envOr("PHOTOS_DIR", "/photos")
	s := &server{
		publicDir: filepath.Join(photosDir, "public"),
		secretDir: filepath.Join(photosDir, "secret"),
	}

	os.MkdirAll(s.publicDir, 0755)
	os.MkdirAll(s.secretDir, 0755)

	gallery.SeedIfEmpty(s.publicDir, gallery.BirdSeedURLs)
	gallery.SeedIfEmpty(s.secretDir, gallery.CatSeedURLs)

	tmpl = template.Must(template.ParseFS(assets, "templates/*.html"))

	mux := http.NewServeMux()

	// Static assets
	mux.Handle("GET /static/", http.FileServerFS(assets))

	// Public gallery — no auth
	mux.HandleFunc("GET /", s.publicGallery)
	mux.HandleFunc("GET /photos/public/{file}", s.servePublicPhoto)

	// Secret album
	mux.HandleFunc("GET /s", s.secretLogin)
	mux.HandleFunc("POST /s/auth", s.secretAuth)
	mux.HandleFunc("GET /s/gallery", auth.RequireSecret(s.secretGallery))
	mux.HandleFunc("GET /photos/secret/{file}", s.serveSecretPhoto)

	// GitHub OAuth
	mux.HandleFunc("GET /auth/login", auth.LoginHandler)
	mux.HandleFunc("GET /auth/callback", auth.CallbackHandler)

	// Admin — GitHub OAuth required
	mux.HandleFunc("GET /admin", auth.RequireAdmin(s.adminPortal))
	mux.HandleFunc("POST /admin/upload", auth.RequireAdmin(s.adminUpload))
	mux.HandleFunc("DELETE /admin/photo", auth.RequireAdmin(s.adminDelete))

	// Utility
	mux.HandleFunc("GET /logout", s.logout)
	mux.HandleFunc("GET /health", health)

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

// ── Public gallery ──────────────────────────────────────────────────────────

func (s *server) publicGallery(w http.ResponseWriter, r *http.Request) {
	photos := gallery.Shuffle(gallery.ListImages(s.publicDir))
	render(w, "public.html", map[string]any{"Photos": photos})
}

func (s *server) servePublicPhoto(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	serveImage(w, r, s.publicDir, name)
}

// ── Secret album ────────────────────────────────────────────────────────────

func (s *server) secretLogin(w http.ResponseWriter, r *http.Request) {
	if auth.IsSecretAuthed(r) {
		http.Redirect(w, r, "/s/gallery", http.StatusFound)
		return
	}
	render(w, "secret_login.html", map[string]any{})
}

func (s *server) secretAuth(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	submitted := r.FormValue("password")
	expected := os.Getenv("SECRET_ALBUM_PASSWORD")

	if subtle.ConstantTimeCompare([]byte(submitted), []byte(expected)) != 1 {
		render(w, "secret_login.html", map[string]any{"Error": "Incorrect password."})
		return
	}
	auth.SetSecretCookie(w)
	http.Redirect(w, r, "/s/gallery", http.StatusFound)
}

func (s *server) secretGallery(w http.ResponseWriter, r *http.Request) {
	photos := gallery.ListImages(s.secretDir)
	render(w, "secret.html", map[string]any{"Photos": photos})
}

func (s *server) serveSecretPhoto(w http.ResponseWriter, r *http.Request) {
	if !auth.IsSecretAuthed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := r.PathValue("file")
	serveImage(w, r, s.secretDir, name)
}

// ── Admin ───────────────────────────────────────────────────────────────────

func (s *server) adminPortal(w http.ResponseWriter, r *http.Request) {
	render(w, "admin.html", map[string]any{
		"User":         auth.GetAdminUser(r),
		"PublicPhotos": gallery.ListImages(s.publicDir),
		"SecretPhotos": gallery.ListImages(s.secretDir),
	})
}

func (s *server) adminUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(200 << 20); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	album := r.FormValue("album")
	dir := s.albumDir(album)
	if dir == "" {
		http.Error(w, "invalid album", http.StatusBadRequest)
		return
	}

	allowed := map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true}
	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 {
		http.Error(w, "missing file", http.StatusBadRequest)
		return
	}

	for _, header := range headers {
		ext := strings.ToLower(filepath.Ext(header.Filename))
		if !allowed[ext] {
			log.Printf("upload skipped unsupported type: %s", header.Filename)
			continue
		}
		file, err := header.Open()
		if err != nil {
			log.Printf("upload open error %s: %v", header.Filename, err)
			continue
		}
		name := filepath.Base(header.Filename)
		dest := filepath.Join(dir, name)
		f, err := os.Create(dest)
		if err != nil {
			file.Close()
			log.Printf("upload create error %s: %v", name, err)
			continue
		}
		io.Copy(f, file)
		f.Close()
		file.Close()
		log.Printf("uploaded %s to %s", name, album)
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *server) adminDelete(w http.ResponseWriter, r *http.Request) {
	album := r.URL.Query().Get("album")
	name := r.URL.Query().Get("name")
	dir := s.albumDir(album)
	if dir == "" || name == "" {
		http.Error(w, "invalid params", http.StatusBadRequest)
		return
	}

	// Prevent path traversal
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || name == ".." {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}

	path := filepath.Join(dir, filepath.Base(name))
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
		} else {
			http.Error(w, "server error", http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Utility ─────────────────────────────────────────────────────────────────

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	auth.ClearCookies(w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *server) albumDir(album string) string {
	switch album {
	case "public":
		return s.publicDir
	case "secret":
		return s.secretDir
	}
	return ""
}

func serveImage(w http.ResponseWriter, r *http.Request, dir, name string) {
	// Prevent path traversal
	if strings.Contains(name, "/") || strings.Contains(name, "\\") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	path := filepath.Join(dir, filepath.Base(name))
	ext := strings.ToLower(filepath.Ext(name))
	ct := mime.TypeByExtension(ext)
	if ct == "" {
		ct = "image/jpeg"
	}
	w.Header().Set("Content-Type", ct)
	http.ServeFile(w, r, path)
}

func render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("template error %s: %v", name, err)
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
