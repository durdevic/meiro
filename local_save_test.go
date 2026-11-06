package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/elianiva/meiro/internal/spotifyimport"
	"github.com/elianiva/meiro/youtube"
)

func TestLocalSongSaveToggle(t *testing.T) {
	resetFakes()
	a := newTestApp(t)
	dir := t.TempDir()
	a.spotify.folder = dir
	a.spotify.ready = true
	a.spotify.lib = spotifyimport.Library{Source: dir}

	item := youtube.MusicItem{
		ID:        "youtube:track:test-song-1",
		VideoID:   "test-song-1",
		Title:     "Awesome Test Track",
		Artist:    "Cool Band",
		Artists:   "Cool Band",
		Album:     "Greatest Album",
		Duration:  "3:30",
		Thumbnail: "https://example.com/art.jpg",
		Kind:      youtube.KindTrack,
	}

	if a.isLocalSongSaved(item) {
		t.Fatal("track should not be saved yet")
	}

	// Toggle save
	a.toggleLocalSaveSong(item)
	if !a.isLocalSongSaved(item) {
		t.Fatal("track should be saved after toggle")
	}

	csvPath := filepath.Join(dir, "Liked_Songs.csv")
	if _, err := os.Stat(csvPath); err != nil {
		t.Fatalf("Liked_Songs.csv was not created: %v", err)
	}

	// Toggle remove
	a.toggleLocalSaveSong(item)
	if a.isLocalSongSaved(item) {
		t.Fatal("track should not be saved after second toggle")
	}
}

func TestLocalPlaylistSaveToggle(t *testing.T) {
	resetFakes()
	a := newTestApp(t)
	dir := t.TempDir()
	a.spotify.folder = dir
	a.spotify.ready = true
	a.spotify.lib = spotifyimport.Library{Source: dir}

	a.page.detail.title = "Epic Vibes"
	a.page.playable = []youtube.MusicItem{
		{
			VideoID:  "vid-1",
			Title:    "Song One",
			Artists:  "Artist One",
			Duration: "2:45",
			Kind:     youtube.KindTrack,
		},
		{
			VideoID:  "vid-2",
			Title:    "Song Two",
			Artists:  "Artist Two",
			Duration: "3:15",
			Kind:     youtube.KindTrack,
		},
	}

	if _, saved := a.isLocalPlaylistSaved("Epic Vibes"); saved {
		t.Fatal("playlist should not be saved yet")
	}

	// Toggle save
	a.toggleLocalSavePlaylist("Epic Vibes")
	id, saved := a.isLocalPlaylistSaved("Epic Vibes")
	if !saved || id == "" {
		t.Fatal("playlist should be saved")
	}

	csvPath := filepath.Join(dir, "Epic Vibes.csv")
	if _, err := os.Stat(csvPath); err != nil {
		t.Fatalf("playlist CSV was not created: %v", err)
	}

	// Toggle remove
	a.toggleLocalSavePlaylist("Epic Vibes")
	if _, saved := a.isLocalPlaylistSaved("Epic Vibes"); saved {
		t.Fatal("playlist should be removed after second toggle")
	}
}

func TestLocalArtistFollowToggle(t *testing.T) {
	resetFakes()
	a := newTestApp(t)
	dir := t.TempDir()
	a.spotify.folder = dir
	a.spotify.ready = true
	a.spotify.lib = spotifyimport.Library{Source: dir}

	artistName := "Stellar Artist"
	artistID := "UCtest123"

	if a.isLocalArtistSaved(artistName, artistID) {
		t.Fatal("artist should not be followed yet")
	}

	a.toggleLocalSaveArtist(artistName, artistID, "https://example.com/artist.jpg")
	if !a.isLocalArtistSaved(artistName, artistID) {
		t.Fatal("artist should be followed after toggle")
	}

	csvPath := filepath.Join(dir, "Followed_Artists.csv")
	if _, err := os.Stat(csvPath); err != nil {
		t.Fatalf("Followed_Artists.csv was not created: %v", err)
	}

	a.toggleLocalSaveArtist(artistName, artistID, "https://example.com/artist.jpg")
	if a.isLocalArtistSaved(artistName, artistID) {
		t.Fatal("artist should be unfollowed after second toggle")
	}
}

func TestHeroSaveButtonUI(t *testing.T) {
	resetFakes()
	a := newTestApp(t)
	dir := t.TempDir()
	a.spotify.folder = dir
	a.spotify.ready = true
	a.spotify.lib = spotifyimport.Library{Source: dir}

	a.router.Push("/album/MPREb_test")
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()

	// Look for the "Save" button in the album hero
	if !tt.HasText("Save") {
		t.Fatalf("expected Save button in hero: %q", tt.Texts())
	}

	if err := tt.Click("Save"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	// Should now show "Saved"
	if !tt.HasText("Saved") {
		t.Fatalf("expected Saved button in hero: %q", tt.Texts())
	}
}

func TestSongRowSaveButtonUI(t *testing.T) {
	resetFakes()
	a := newTestApp(t)
	dir := t.TempDir()
	a.spotify.folder = dir
	a.spotify.ready = true
	a.spotify.lib = spotifyimport.Library{Source: dir}

	a.router.Push("/album/MPREb_test")
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()

	// Hover over the first song row
	row, ok := tt.Find("Album Track One")
	if !ok {
		t.Fatalf("no row Album Track One: %q", tt.Texts())
	}
	tt.Move(row.X+row.W/2, row.Y+row.H/2)
	tt.Frame()

	saveLabel := "Save Album Track One to library"
	if err := tt.Click(saveLabel); err != nil {
		t.Fatalf("could not click save button on row: %v", err)
	}
	tt.Frame()

	// Song should now be saved
	track := a.page.feed.sections[0].Items[0]
	if !a.isLocalSongSaved(track) {
		t.Fatal("track was not saved after clicking save button on row")
	}

	removeLabel := "Remove Album Track One from library"
	if err := tt.Click(removeLabel); err != nil {
		t.Fatalf("could not click remove button on row: %v", err)
	}
	tt.Frame()

	if a.isLocalSongSaved(track) {
		t.Fatal("track was not removed after clicking remove button on row")
	}
}

