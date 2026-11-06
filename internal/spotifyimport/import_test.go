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

func TestAppendAndRemoveTrack(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "Liked_Songs.csv")
	track1 := Track{
		URI:        "spotify:track:123",
		Title:      "First Song",
		Artists:    "First Artist",
		Album:      "First Album",
		DurationMs: 180000,
		Cover:      "https://example.com/cover1.jpg",
	}
	track2 := Track{
		URI:        "youtube:track:vid-456",
		Title:      "Second Song",
		Artists:    "Second Artist",
		Album:      "Second Album",
		DurationMs: 200000,
		Cover:      "https://example.com/cover2.jpg",
	}
	if err := AppendTrack(csvPath, track1); err != nil {
		t.Fatal(err)
	}
	if err := AppendTrack(csvPath, track2); err != nil {
		t.Fatal(err)
	}
	// duplicate append should no-op
	if err := AppendTrack(csvPath, track1); err != nil {
		t.Fatal(err)
	}
	parsed, err := parseFile(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(parsed))
	}
	if parsed[0].Title != "First Song" || parsed[1].Title != "Second Song" {
		t.Fatalf("unexpected tracks: %#v", parsed)
	}

	// Remove track1
	if err := RemoveTrack(csvPath, track1.URI, track1.Title, track1.Artists); err != nil {
		t.Fatal(err)
	}
	afterRemove, err := parseFile(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterRemove) != 1 || afterRemove[0].Title != "Second Song" {
		t.Fatalf("expected 1 track remaining, got %#v", afterRemove)
	}
}

func TestWritePlaylist(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "New_Playlist.csv")
	tracks := []Track{
		{URI: "spotify:track:a", Title: "Song A", Artists: "Artist A", DurationMs: 120000},
		{URI: "spotify:track:b", Title: "Song B", Artists: "Artist B", DurationMs: 150000},
	}
	if err := WritePlaylist(csvPath, tracks); err != nil {
		t.Fatal(err)
	}
	got, err := parseFile(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Title != "Song A" || got[1].Title != "Song B" {
		t.Fatalf("unexpected parsed playlist: %#v", got)
	}
}

func TestArtistSaveAndRemove(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "Followed_Artists.csv")
	artist1 := ArtistRecord{
		URI:        "artist-1",
		Name:       "Test Band",
		Thumbnail:  "https://example.com/art.jpg",
		FollowedAt: "2026-11-06T12:00:00Z",
	}
	if err := AppendArtist(csvPath, artist1); err != nil {
		t.Fatal(err)
	}
	// duplicate
	if err := AppendArtist(csvPath, artist1); err != nil {
		t.Fatal(err)
	}
	list, err := ReadArtists(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "Test Band" {
		t.Fatalf("expected 1 artist, got %#v", list)
	}
	if err := RemoveArtist(csvPath, "Test Band", "artist-1"); err != nil {
		t.Fatal(err)
	}
	listAfter, err := ReadArtists(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(listAfter) != 0 {
		t.Fatalf("expected 0 artists after remove, got %#v", listAfter)
	}
}

func TestSanitizeFileName(t *testing.T) {
	if got := SanitizeFileName("Rock / Pop: Hits *2026*?"); got != "Rock _ Pop_ Hits _2026" {
		t.Fatalf("unexpected sanitize: %q", got)
	}
	if got := SanitizeFileName("   "); got != "Untitled" {
		t.Fatalf("unexpected sanitize blank: %q", got)
	}
}

