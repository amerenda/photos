package gallery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// AlbumMeta holds metadata for a single album.
type AlbumMeta struct {
	Secret bool `json:"secret"`
}

// Albums maps album name → metadata.
type Albums map[string]AlbumMeta

// LoadAlbums reads albums.json from photosDir. Returns empty map if not found.
func LoadAlbums(photosDir string) (Albums, error) {
	data, err := os.ReadFile(filepath.Join(photosDir, "albums.json"))
	if os.IsNotExist(err) {
		return Albums{}, nil
	}
	if err != nil {
		return nil, err
	}
	var a Albums
	return a, json.Unmarshal(data, &a)
}

// SaveAlbums writes albums.json to photosDir.
func SaveAlbums(photosDir string, albums Albums) error {
	data, err := json.MarshalIndent(albums, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(photosDir, "albums.json"), data, 0644)
}

// CreateAlbum creates a new album directory and registers it in albums.json.
func CreateAlbum(photosDir, name string, secret bool) error {
	albums, err := LoadAlbums(photosDir)
	if err != nil {
		return err
	}
	if _, exists := albums[name]; exists {
		return fmt.Errorf("album %q already exists", name)
	}
	if err := os.MkdirAll(filepath.Join(photosDir, name), 0755); err != nil {
		return err
	}
	albums[name] = AlbumMeta{Secret: secret}
	return SaveAlbums(photosDir, albums)
}

// DeleteAlbum removes an album directory and its registration.
func DeleteAlbum(photosDir, name string) error {
	albums, err := LoadAlbums(photosDir)
	if err != nil {
		return err
	}
	delete(albums, name)
	if err := os.RemoveAll(filepath.Join(photosDir, name)); err != nil {
		return err
	}
	return SaveAlbums(photosDir, albums)
}

// SortedNames returns album names in sorted order.
func SortedNames(albums Albums) []string {
	names := make([]string, 0, len(albums))
	for n := range albums {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// PhotoURLs returns shuffled /photos/<album>/<file> URL strings for all
// albums matching the given secret flag.
func PhotoURLs(photosDir string, albums Albums, secret bool) []string {
	var urls []string
	for name, meta := range albums {
		if meta.Secret != secret {
			continue
		}
		dir := filepath.Join(photosDir, name)
		for _, img := range ListImages(dir) {
			urls = append(urls, "/photos/"+name+"/"+img)
		}
	}
	return Shuffle(urls)
}

// MigrateIfNeeded converts the old public/secret directory layout to
// the album-based layout on first run. Creates "photos" (public) and
// "nsfw" (secret) albums, moving existing files into them.
func MigrateIfNeeded(photosDir string) error {
	albumsPath := filepath.Join(photosDir, "albums.json")
	if _, err := os.Stat(albumsPath); err == nil {
		return nil // already migrated
	}

	albums := Albums{
		"photos": {Secret: false},
		"nsfw":   {Secret: true},
	}

	migrate := func(oldName, newName string) error {
		oldDir := filepath.Join(photosDir, oldName)
		newDir := filepath.Join(photosDir, newName)
		if err := os.MkdirAll(newDir, 0755); err != nil {
			return err
		}
		entries, err := os.ReadDir(oldDir)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if err := os.Rename(
				filepath.Join(oldDir, e.Name()),
				filepath.Join(newDir, e.Name()),
			); err != nil {
				return err
			}
		}
		return os.Remove(oldDir)
	}

	if err := migrate("public", "photos"); err != nil {
		return fmt.Errorf("migrate public→photos: %w", err)
	}
	if err := migrate("secret", "nsfw"); err != nil {
		return fmt.Errorf("migrate secret→nsfw: %w", err)
	}

	return SaveAlbums(photosDir, albums)
}
