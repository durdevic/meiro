package spotifyimport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sample = "Track URI,Track Name,Album Name,Artist Name(s),Release Date,Duration (ms)\n" +
	"spotify:track:abc,\"Tales of Endurance, Pt. 4\",\"Road To Rouen\",\"Supergrass\",2005-06-13,331426\n" +
	"spotify:track:def,Jamaican,\"Jamaican\",\"HUGEL;SOLTO\",2025-11-07,156393\n"

func TestParseSpotifyExport(t *testing.T) {
	tracks, err := parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 {
		t.Fatalf("tracks = %d", len(tracks))
	}
	if tracks[0].Title != "Tales of Endurance, Pt. 4" || tracks[0].Artists != "Supergrass" || tracks[0].DurationMs != 331426 {
		t.Fatalf("first track = %#v", tracks[0])
	}
	if FirstArtist(tracks[1].Artists) != "HUGEL" || Query(tracks[1]) != "HUGEL Jamaican" {
		t.Fatalf("query = %q from %#v", Query(tracks[1]), tracks[1])
	}
	if FormatDuration(331426) != "5:31" {
		t.Fatalf("duration = %s", FormatDuration(331426))
	}
}

func TestImportDirKeepsEarlierMatches(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Chill.csv"), []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := Library{Playlists: []Playlist{{Tracks: []Track{{URI: "spotify:track:abc", VideoID: "yt1", Cover: "https://img.test/from-library.jpg"}}}}}
	csvPath := filepath.Join(dir, "Chill.csv")
	if err := WriteCoverURL(csvPath, "spotify:track:abc", "https://img.test/cover.jpg"); err != nil {
		t.Fatal(err)
	}
	lib, skipped, err := ImportDir(dir, previous)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 || len(lib.Playlists) != 1 {
		t.Fatalf("playlists=%d skipped=%d", len(lib.Playlists), skipped)
	}
	got := lib.Playlists[0]
	if got.Name != "Chill" || got.Tracks[0].VideoID != "yt1" || got.Tracks[0].Cover != "https://img.test/cover.jpg" {
		t.Fatalf("playlist = %#v", got)
	}
	path := filepath.Join(t.TempDir(), "spotify.json")
	if err := Save(path, lib); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "cover.jpg") || strings.Contains(string(saved), "from-library") {
		t.Fatalf("library file stored a cover: %s", saved)
	}
	if loaded := Load(path); loaded.Playlists[0].Tracks[0].VideoID != "yt1" || loaded.Playlists[0].Tracks[0].Cover != "" {
		t.Fatalf("loaded = %#v", loaded)
	}
	loaded := Load(path)
	loaded.ReadCovers()
	if loaded.Playlists[0].Tracks[0].Cover != "https://img.test/cover.jpg" {
		t.Fatalf("cover from csv = %q", loaded.Playlists[0].Tracks[0].Cover)
	}
}

func TestCopyCSVsKeepsTheCoverColumn(t *testing.T) {
	from := t.TempDir()
	csvPath := filepath.Join(from, "Chill.csv")
	if err := os.WriteFile(csvPath, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteCoverURL(csvPath, "spotify:track:abc", "https://img.test/cover.jpg"); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(t.TempDir(), "spotify")
	if err := CopyCSVs(from, to); err != nil {
		t.Fatal(err)
	}
	lib, _, err := ImportDir(to, Library{})
	if err != nil {
		t.Fatal(err)
	}
	lib.ReadCovers()
	if lib.Source != to || lib.Playlists[0].Tracks[0].Cover != "https://img.test/cover.jpg" {
		t.Fatalf("copied library = %#v", lib)
	}
}

func TestPickPrefersTheSongOverAVideo(t *testing.T) {
	track := Track{Title: "Jamaican", Artists: "HUGEL", DurationMs: 156393}
	candidates := []Candidate{
		{Title: "Jamaican (Official Video)", Artist: "HUGEL", Duration: "2:36", Video: true, Song: true},
		{Title: "Jamaican", Artist: "HUGEL", Duration: "2:36", Song: true},
		{Title: "Something Else", Artist: "Other", Duration: "2:36", Song: true},
	}
	got, ok := Pick(track, candidates)
	if !ok || got != 1 {
		t.Fatalf("pick = %d ok=%t", got, ok)
	}
	if _, ok := Pick(track, []Candidate{{Title: "Unrelated", Song: true}}); ok {
		t.Fatal("an unrelated title was accepted")
	}
	if ParseClock("1:02:03") != 1*time.Hour+2*time.Minute+3*time.Second {
		t.Fatalf("clock = %s", ParseClock("1:02:03"))
	}
}
