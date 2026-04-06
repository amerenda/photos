package gallery

import (
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true,
	".gif": true, ".webp": true,
}

// ListImages returns a list of image filenames in dir.
func ListImages(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var images []string
	for _, e := range entries {
		if !e.IsDir() && imageExts[strings.ToLower(filepath.Ext(e.Name()))] {
			images = append(images, e.Name())
		}
	}
	return images
}

// Shuffle returns a new slice with elements in random order.
func Shuffle(images []string) []string {
	out := make([]string, len(images))
	copy(out, images)
	rand.New(rand.NewSource(time.Now().UnixNano())).Shuffle(len(out), func(i, j int) {
		out[i], out[j] = out[j], out[i]
	})
	return out
}

// SeedIfEmpty downloads seed photos into dir if it contains no images.
func SeedIfEmpty(dir string, urls []SeedPhoto) {
	if len(ListImages(dir)) > 0 {
		return
	}
	log.Printf("seeding %s with %d photos", dir, len(urls))
	client := &http.Client{Timeout: 30 * time.Second}
	for _, s := range urls {
		dest := filepath.Join(dir, s.Name)
		if err := download(client, s.URL, dest); err != nil {
			log.Printf("seed download failed %s: %v", s.URL, err)
		} else {
			log.Printf("seeded %s", s.Name)
		}
	}
}

func download(client *http.Client, url, dest string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			f.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return nil
}

// SeedPhoto is a name + URL pair for seeding.
type SeedPhoto struct {
	Name string
	URL  string
}

// BirdSeedURLs are free-to-use bird photos from Wikimedia Commons.
var BirdSeedURLs = []SeedPhoto{
	{
		Name: "atlantic-puffin.jpg",
		URL:  "https://upload.wikimedia.org/wikipedia/commons/thumb/8/88/Atlantic_Puffin_in_flight_-_USFWS.jpg/1280px-Atlantic_Puffin_in_flight_-_USFWS.jpg",
	},
	{
		Name: "flamingo.jpg",
		URL:  "https://upload.wikimedia.org/wikipedia/commons/thumb/b/b8/Phoenicopterus_ruber_in_Houston_zoo.jpg/1280px-Phoenicopterus_ruber_in_Houston_zoo.jpg",
	},
	{
		Name: "great-tit.jpg",
		URL:  "https://upload.wikimedia.org/wikipedia/commons/thumb/6/6a/Parus_major_m.jpg/1280px-Parus_major_m.jpg",
	},
	{
		Name: "kingfisher.jpg",
		URL:  "https://upload.wikimedia.org/wikipedia/commons/thumb/9/9f/Kingfisher-2013-Smudge9000.jpg/1280px-Kingfisher-2013-Smudge9000.jpg",
	},
	{
		Name: "snowy-owl.jpg",
		URL:  "https://upload.wikimedia.org/wikipedia/commons/thumb/d/de/Snowy_Owl_%28240866707%29.jpeg/1280px-Snowy_Owl_%28240866707%29.jpeg",
	},
}

// CatSeedURLs are free-to-use cat photos from Wikimedia Commons.
var CatSeedURLs = []SeedPhoto{
	{
		Name: "orange-tabby.jpg",
		URL:  "https://upload.wikimedia.org/wikipedia/commons/thumb/4/4d/Cat_November_2010-1a.jpg/1280px-Cat_November_2010-1a.jpg",
	},
	{
		Name: "kitten.jpg",
		URL:  "https://upload.wikimedia.org/wikipedia/commons/thumb/b/bb/Kittyply_edit1.jpg/1280px-Kittyply_edit1.jpg",
	},
	{
		Name: "tabby-leaves.jpg",
		URL:  "https://upload.wikimedia.org/wikipedia/commons/thumb/6/65/Orange_tabby_cat_sitting_on_fallen_leaves-Hisashi-01A.jpg/1280px-Orange_tabby_cat_sitting_on_fallen_leaves-Hisashi-01A.jpg",
	},
	{
		Name: "black-cat.jpg",
		URL:  "https://upload.wikimedia.org/wikipedia/commons/thumb/a/a5/Klara-%28cat%29.jpg/1280px-Klara-%28cat%29.jpg",
	},
	{
		Name: "cat-portrait.jpg",
		URL:  "https://upload.wikimedia.org/wikipedia/commons/thumb/3/3a/Cat03.jpg/1280px-Cat03.jpg",
	},
}
