package main

import (
	"crypto/subtle"
	"embed"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/amerenda/photos/internal/auth"
	"github.com/amerenda/photos/internal/gallery"
)

//go:embed templates static
var assets embed.FS

var tmpl *template.Template

var safeAlbumName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type server struct {
	photosDir string
}

func main() {
	photosDir := envOr("PHOTOS_DIR", "/photos")
	s := &server{photosDir: photosDir}

	if err := gallery.MigrateIfNeeded(photosDir); err != nil {
		log.Fatalf("migration failed: %v", err)
	}

	gallery.SeedIfEmpty(filepath.Join(photosDir, "photos"), gallery.BirdSeedURLs)
	gallery.SeedIfEmpty(filepath.Join(photosDir, "nsfw"), gallery.CatSeedURLs)

	tmpl = template.Must(template.ParseFS(assets, "templates/*.html"))

	mux := http.NewServeMux()

	mux.Handle("GET /static/", http.FileServerFS(assets))

	// Public gallery
	mux.HandleFunc("GET /", s.publicGallery)

	// Secret album (password)
	mux.HandleFunc("GET /s", s.secretLogin)
	mux.HandleFunc("POST /s/auth", s.secretAuth)
	mux.HandleFunc("GET /s/gallery", auth.RequireSecret(s.secretGallery))

	// Puzzle album (Konami code + password)
	mux.HandleFunc("POST /p/auth", s.puzzleAuth)
	mux.HandleFunc("GET /p", auth.RequirePuzzle(s.puzzleGallery))
	mux.HandleFunc("GET /reward", auth.RequirePuzzle(s.puzzleRewardGallery))

	// Photo serving (album-aware auth)
	mux.HandleFunc("GET /photos/{album}/{file}", s.servePhoto)

	// GitHub OAuth
	mux.HandleFunc("GET /auth/login", auth.LoginHandler)
	mux.HandleFunc("GET /auth/callback", auth.CallbackHandler)

	// Admin
	mux.HandleFunc("GET /admin", auth.RequireAdmin(s.adminPortal))
	mux.HandleFunc("POST /admin/upload", auth.RequireAdmin(s.adminUpload))
	mux.HandleFunc("DELETE /admin/photo", auth.RequireAdmin(s.adminDelete))
	mux.HandleFunc("POST /admin/album", auth.RequireAdmin(s.adminCreateAlbum))
	mux.HandleFunc("DELETE /admin/album", auth.RequireAdmin(s.adminDeleteAlbum))
	mux.HandleFunc("POST /admin/photo/move", auth.RequireAdmin(s.adminMovePhoto))
	mux.HandleFunc("POST /admin/photo/copy", auth.RequireAdmin(s.adminCopyPhoto))

	mux.HandleFunc("GET /logout", s.logout)
	mux.HandleFunc("GET /health", health)

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

// ── Public gallery ──────────────────────────────────────────────────────────

func (s *server) publicGallery(w http.ResponseWriter, r *http.Request) {
	albums, _ := gallery.LoadAlbums(s.photosDir)
	photos := gallery.PhotoURLs(s.photosDir, albums, gallery.AccessPublic)
	render(w, "public.html", map[string]any{"Photos": photos})
}

// ── Secret album (password) ─────────────────────────────────────────────────

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
	if subtle.ConstantTimeCompare(
		[]byte(r.FormValue("password")),
		[]byte(os.Getenv("SECRET_ALBUM_PASSWORD")),
	) != 1 {
		render(w, "secret_login.html", map[string]any{"Error": "Incorrect password."})
		return
	}
	auth.SetSecretCookie(w)
	http.Redirect(w, r, "/s/gallery", http.StatusFound)
}

func (s *server) secretGallery(w http.ResponseWriter, r *http.Request) {
	albums, _ := gallery.LoadAlbums(s.photosDir)
	photos := gallery.PhotoURLs(s.photosDir, albums, gallery.AccessSecret)
	render(w, "secret.html", map[string]any{"Photos": photos})
}

// ── Puzzle album (Konami code + password) ───────────────────────────────────

func (s *server) puzzleAuth(w http.ResponseWriter, r *http.Request) {
	auth.SetPuzzleCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) puzzleGallery(w http.ResponseWriter, r *http.Request) {
	albums, _ := gallery.LoadAlbums(s.photosDir)
	photos := gallery.PhotoURLs(s.photosDir, albums, gallery.AccessPuzzle)
	render(w, "puzzle.html", map[string]any{"Photos": photos})
}

func (s *server) puzzleRewardGallery(w http.ResponseWriter, r *http.Request) {
	dir := filepath.Join(s.photosDir, "puzzle_reward")
	photos := gallery.ListImages(dir)
	var urls []string
	for _, p := range photos {
		urls = append(urls, "/photos/puzzle_reward/"+p)
	}
	render(w, "puzzle.html", map[string]any{"Photos": gallery.Shuffle(urls)})
}

// ── Photo serving ────────────────────────────────────────────────────────────

func (s *server) servePhoto(w http.ResponseWriter, r *http.Request) {
	albumName := r.PathValue("album")
	file := r.PathValue("file")

	if !safeAlbumName.MatchString(albumName) {
		http.Error(w, "invalid album", http.StatusBadRequest)
		return
	}

	albums, err := gallery.LoadAlbums(s.photosDir)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	meta, ok := albums[albumName]
	if !ok {
		http.NotFound(w, r)
		return
	}

	isAdmin := auth.GetAdminUser(r) != ""
	switch meta.Access {
	case gallery.AccessSecret:
		if !isAdmin && !auth.IsSecretAuthed(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	case gallery.AccessPuzzle:
		if !isAdmin && !auth.IsPuzzleAuthed(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	serveImage(w, r, filepath.Join(s.photosDir, albumName), file)
}

// ── Admin ───────────────────────────────────────────────────────────────────

type albumView struct {
	Name   string
	Access string
	Photos []string
}

func (s *server) adminPortal(w http.ResponseWriter, r *http.Request) {
	albums, _ := gallery.LoadAlbums(s.photosDir)
	var views []albumView
	for _, name := range gallery.SortedNames(albums) {
		meta := albums[name]
		views = append(views, albumView{
			Name:   name,
			Access: meta.Access,
			Photos: gallery.ListImages(filepath.Join(s.photosDir, name)),
		})
	}
	render(w, "admin.html", map[string]any{
		"User":   auth.GetAdminUser(r),
		"Albums": views,
	})
}

func (s *server) adminCreateAlbum(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	if !safeAlbumName.MatchString(name) {
		http.Error(w, "invalid album name (letters, digits, - _ only)", http.StatusBadRequest)
		return
	}
	access := r.FormValue("access")
	if access != gallery.AccessPublic && access != gallery.AccessSecret && access != gallery.AccessPuzzle {
		access = gallery.AccessPublic
	}
	if err := gallery.CreateAlbum(s.photosDir, name, access); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	log.Printf("created album %q access=%s", name, access)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) adminDeleteAlbum(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if !safeAlbumName.MatchString(name) {
		http.Error(w, "invalid album name", http.StatusBadRequest)
		return
	}
	if err := gallery.DeleteAlbum(s.photosDir, name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("deleted album %q", name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) adminUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(200 << 20); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	albumName := r.FormValue("album")
	if !safeAlbumName.MatchString(albumName) {
		http.Error(w, "invalid album name", http.StatusBadRequest)
		return
	}
	albums, _ := gallery.LoadAlbums(s.photosDir)
	if _, ok := albums[albumName]; !ok {
		http.Error(w, "album not found", http.StatusBadRequest)
		return
	}
	dir := filepath.Join(s.photosDir, albumName)

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
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			file.Close()
			log.Printf("upload create error %s: %v", name, err)
			continue
		}
		io.Copy(f, file)
		f.Close()
		file.Close()
		log.Printf("uploaded %s to %s", name, albumName)
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *server) adminDelete(w http.ResponseWriter, r *http.Request) {
	albumName := r.URL.Query().Get("album")
	name := r.URL.Query().Get("name")
	if !safeAlbumName.MatchString(albumName) || name == "" {
		http.Error(w, "invalid params", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(name, "/\\") || name == ".." {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}
	if err := os.Remove(filepath.Join(s.photosDir, albumName, filepath.Base(name))); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
		} else {
			http.Error(w, "server error", http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) adminMovePhoto(w http.ResponseWriter, r *http.Request) { s.transferPhoto(w, r, true) }
func (s *server) adminCopyPhoto(w http.ResponseWriter, r *http.Request) { s.transferPhoto(w, r, false) }

func (s *server) transferPhoto(w http.ResponseWriter, r *http.Request, move bool) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	fromAlbum := r.FormValue("from_album")
	toAlbum := r.FormValue("to_album")
	name := r.FormValue("name")

	if !safeAlbumName.MatchString(fromAlbum) || !safeAlbumName.MatchString(toAlbum) {
		http.Error(w, "invalid album name", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(name, "/\\") || name == ".." || name == "" {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}

	albums, _ := gallery.LoadAlbums(s.photosDir)
	if _, ok := albums[fromAlbum]; !ok {
		http.Error(w, "source album not found", http.StatusBadRequest)
		return
	}
	if _, ok := albums[toAlbum]; !ok {
		http.Error(w, "target album not found", http.StatusBadRequest)
		return
	}

	safeName := filepath.Base(name)
	src := filepath.Join(s.photosDir, fromAlbum, safeName)
	dst := filepath.Join(s.photosDir, toAlbum, safeName)

	if move {
		if err := os.Rename(src, dst); err != nil {
			http.Error(w, fmt.Sprintf("move failed: %v", err), http.StatusInternalServerError)
			return
		}
		log.Printf("moved %s: %s → %s", safeName, fromAlbum, toAlbum)
	} else {
		if err := copyFile(src, dst); err != nil {
			http.Error(w, fmt.Sprintf("copy failed: %v", err), http.StatusInternalServerError)
			return
		}
		log.Printf("copied %s: %s → %s", safeName, fromAlbum, toAlbum)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Utility ─────────────────────────────────────────────────────────────────

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	auth.ClearCookies(w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func health(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }

func serveImage(w http.ResponseWriter, r *http.Request, dir, name string) {
	if strings.ContainsAny(name, "/\\") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	path := filepath.Join(dir, filepath.Base(name))
	ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if ct == "" {
		ct = "image/jpeg"
	}
	w.Header().Set("Content-Type", ct)
	http.ServeFile(w, r, path)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
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
