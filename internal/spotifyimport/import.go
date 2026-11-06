// Package spotifyimport reads playlists exported from Spotify as CSV files
// and picks a YouTube Music song for one of those tracks.
package spotifyimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/elianiva/meiro/internal/fsutil"
)

// Track is one song from a Spotify export.
type Track struct {
	URI        string `json:"uri"`
	Title      string `json:"title"`
	Artists    string `json:"artists"`
	Album      string `json:"album"`
	DurationMs int    `json:"durationMs"`
	// VideoID is the YouTube Music match found the last time this song played.
	VideoID string `json:"videoId,omitempty"`
	// Cover is the artwork URL for this session. It is not stored in the
	// library file; the playlist CSV keeps it in its own column.
	Cover string `json:"-"`
}

// CoverColumn is the playlist CSV column that stores a matched song's artwork URL.
const CoverColumn = "Cover URL"

// Playlist is one exported CSV.
type Playlist struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	File   string  `json:"file"`
	Tracks []Track `json:"tracks"`
}

// Library is every playlist imported from one folder.
type Library struct {
	Source    string     `json:"source"`
	Playlists []Playlist `json:"playlists"`
}

// Candidate is a YouTube Music row a match can be chosen from.
type Candidate struct {
	Title    string
	Artist   string
	Artists  string
	Duration string
	// Video is a music video rather than the song itself.
	Video bool
	// Song is a row that can be played.
	Song bool
}

// ErrNoPlaylists is returned when a folder has no usable exports.
var ErrNoPlaylists = errors.New("no Spotify playlists found")

// Load reads a library from path. A missing file is an empty library.
func Load(path string) Library {
	data, err := os.ReadFile(path)
	if err != nil {
		return Library{}
	}
	var lib Library
	if json.Unmarshal(data, &lib) != nil {
		return Library{}
	}
	return lib
}

// Save writes lib to path.
func Save(path string, lib Library) error {
	data, err := json.Marshal(lib)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, data, 0o600)
}

// Clone returns a copy the caller can write without sharing lib's arrays.
func Clone(lib Library) Library {
	out := lib
	out.Playlists = slices.Clone(lib.Playlists)
	for i := range out.Playlists {
		out.Playlists[i].Tracks = slices.Clone(out.Playlists[i].Tracks)
	}
	return out
}

// PlaylistByID returns the playlist with id.
func (lib Library) PlaylistByID(id string) (Playlist, bool) {
	for _, playlist := range lib.Playlists {
		if playlist.ID == id {
			return playlist, true
		}
	}
	return Playlist{}, false
}

// UsedIDs returns the set of all playlist IDs currently in lib.
func (lib Library) UsedIDs() map[string]struct{} {
	used := make(map[string]struct{}, len(lib.Playlists))
	for _, p := range lib.Playlists {
		used[p.ID] = struct{}{}
	}
	return used
}

// AddTrack adds track to playlist with filename, or creates the playlist if missing.
func (lib *Library) AddTrack(filename, playlistName string, track Track) {
	for i := range lib.Playlists {
		if strings.EqualFold(lib.Playlists[i].File, filename) {
			for _, t := range lib.Playlists[i].Tracks {
				if (track.URI != "" && t.URI == track.URI) || (strings.EqualFold(t.Title, track.Title) && strings.EqualFold(t.Artists, track.Artists)) {
					return
				}
			}
			lib.Playlists[i].Tracks = append(lib.Playlists[i].Tracks, track)
			return
		}
	}
	used := lib.UsedIDs()
	id := PlaylistID(filename, used)
	lib.Playlists = append(lib.Playlists, Playlist{
		ID:     id,
		Name:   playlistName,
		File:   filename,
		Tracks: []Track{track},
	})
}

// RemoveTrack removes a track matching URI or title/artist from the playlist with filename.
func (lib *Library) RemoveTrack(filename, uri, title, artist string) {
	for i := range lib.Playlists {
		if !strings.EqualFold(lib.Playlists[i].File, filename) {
			continue
		}
		tracks := lib.Playlists[i].Tracks[:0]
		for _, t := range lib.Playlists[i].Tracks {
			match := (uri != "" && t.URI == uri) || (strings.EqualFold(t.Title, title) && (artist == "" || strings.EqualFold(t.Artists, artist)))
			if !match {
				tracks = append(tracks, t)
			}
		}
		lib.Playlists[i].Tracks = tracks
		return
	}
}

// RemovePlaylistByID removes the playlist with id.
func (lib *Library) RemovePlaylistByID(id string) {
	lib.Playlists = slices.DeleteFunc(lib.Playlists, func(p Playlist) bool {
		return p.ID == id
	})
}

// SetMatch stores the YouTube song and its cover for the track at index.
func (lib *Library) SetMatch(playlistID string, index int, videoID, cover string) {
	for i := range lib.Playlists {
		if lib.Playlists[i].ID != playlistID {
			continue
		}
		if index < 0 || index >= len(lib.Playlists[i].Tracks) {
			return
		}
		lib.Playlists[i].Tracks[index].VideoID = videoID
		if cover != "" {
			lib.Playlists[i].Tracks[index].Cover = cover
		}
		return
	}
}

// CopyCSVs copies the playlist exports in from into to. to is Meiro's own
// folder, so later cover updates do not need access to the original export.
func CopyCSVs(from, to string) error {
	from, to = filepath.Clean(from), filepath.Clean(to)
	if from == to {
		return nil
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(to, 0o700); err != nil {
		return err
	}
	copied := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".csv") {
			continue
		}
		if err := copyFile(filepath.Join(from, entry.Name()), filepath.Join(to, entry.Name())); err != nil {
			return err
		}
		copied++
	}
	if copied == 0 {
		return fmt.Errorf("%w in %s", ErrNoPlaylists, from)
	}
	return nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(to)
	}
	return err
}

// ImportDir reads every Spotify CSV in dir. Matches already stored in previous
// are kept when the same Spotify track shows up again.
func ImportDir(dir string, previous Library) (Library, int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Library{}, 0, err
	}
	known := savedMatches(previous)
	used := map[string]struct{}{}
	lib := Library{Source: dir}
	skipped := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".csv") {
			continue
		}
		tracks, err := parseFile(filepath.Join(dir, entry.Name()))
		if err != nil || len(tracks) == 0 {
			skipped++
			continue
		}
		for i := range tracks {
			if videoID := known[tracks[i].URI]; videoID != "" {
				tracks[i].VideoID = videoID
			}
		}
		name := DisplayName(entry.Name())
		lib.Playlists = append(lib.Playlists, Playlist{
			ID: PlaylistID(entry.Name(), used), Name: name, File: entry.Name(), Tracks: tracks,
		})
	}
	if len(lib.Playlists) == 0 {
		return Library{}, skipped, fmt.Errorf("%w in %s", ErrNoPlaylists, dir)
	}
	slices.SortFunc(lib.Playlists, func(a, b Playlist) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return lib, skipped, nil
}

func savedMatches(lib Library) map[string]string {
	matches := map[string]string{}
	for _, playlist := range lib.Playlists {
		for _, track := range playlist.Tracks {
			if track.URI != "" && track.VideoID != "" {
				matches[track.URI] = track.VideoID
			}
		}
	}
	return matches
}

// ReadCovers fills each track's cover from the Cover URL column of its CSV.
func (lib *Library) ReadCovers() {
	if lib.Source == "" {
		return
	}
	for i := range lib.Playlists {
		tracks, err := parseFile(filepath.Join(lib.Source, lib.Playlists[i].File))
		if err != nil {
			continue
		}
		covers := map[string]string{}
		for _, track := range tracks {
			if track.URI != "" && track.Cover != "" {
				covers[track.URI] = track.Cover
			}
		}
		for j := range lib.Playlists[i].Tracks {
			if cover := covers[lib.Playlists[i].Tracks[j].URI]; cover != "" {
				lib.Playlists[i].Tracks[j].Cover = cover
			}
		}
	}
}

// WriteCoverURL stores cover on every row of path whose Track URI is trackURI.
// The value is the URL string, added in its own column when the file has none.
func WriteCoverURL(path, trackURI, cover string) error {
	trackURI, cover = strings.TrimSpace(trackURI), strings.TrimSpace(cover)
	if trackURI == "" || cover == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	records, err := reader.ReadAll()
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return errors.New("spotify playlist is empty")
	}
	header := records[0]
	cols := map[string]int{}
	for i, name := range header {
		cols[strings.TrimPrefix(strings.TrimSpace(name), "\uFEFF")] = i
	}
	uriCol, uriOK := cols["Track URI"]
	if !uriOK {
		return errors.New("spotify playlist has no track URIs")
	}
	coverCol, coverOK := cols[CoverColumn]
	if !coverOK {
		header = append(header, CoverColumn)
		records[0] = header
		coverCol = len(header) - 1
	}
	for i := 1; i < len(records); i++ {
		row := records[i]
		if field(row, uriCol) != trackURI {
			continue
		}
		for len(row) <= coverCol {
			row = append(row, "")
		}
		row[coverCol] = cover
		records[i] = row
	}
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	if err := writer.WriteAll(records); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, buf.Bytes(), 0o644)
}

// PlaylistID generates a short unique ID for a playlist file.
func PlaylistID(file string, used map[string]struct{}) string {
	sum := sha256.Sum256([]byte(file))
	id := hex.EncodeToString(sum[:5])
	for {
		if _, taken := used[id]; !taken {
			used[id] = struct{}{}
			return id
		}
		id += "x"
	}
}

// DisplayName turns a file name like "Chill_Vibes.csv" into "Chill Vibes".
func DisplayName(file string) string {
	name := strings.TrimSuffix(file, filepath.Ext(file))
	name = strings.ReplaceAll(name, "_", " ")
	name = strings.TrimSpace(name)
	if name == "" {
		return "Untitled"
	}
	return name
}

// SanitizeFileName cleans a string so it is valid as a filename across platforms.
func SanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	invalid := func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		if r < 32 {
			return '_'
		}
		return r
	}
	clean := strings.Map(invalid, name)
	clean = strings.Trim(clean, " ._")
	if clean == "" {
		return "Untitled"
	}
	return clean
}

// AppendTrack appends track to path. If the CSV file does not exist, it is created
// with standard Spotify-compatible export columns.
func AppendTrack(path string, track Track) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var records [][]string
	var header []string
	cols := map[string]int{}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		r := csv.NewReader(bytes.NewReader(data))
		r.FieldsPerRecord = -1
		r.LazyQuotes = true
		records, _ = r.ReadAll()
	}
	if len(records) > 0 {
		header = records[0]
		for i, name := range header {
			cols[strings.TrimPrefix(strings.TrimSpace(name), "\uFEFF")] = i
		}
	} else {
		header = []string{"Track URI", "Track Name", "Album Name", "Artist Name(s)", "Release Date", "Duration (ms)", CoverColumn}
		records = [][]string{header}
		for i, name := range header {
			cols[name] = i
		}
	}
	uriCol, hasURI := cols["Track URI"]
	titleCol, hasTitle := cols["Track Name"]
	artistCol, hasArtist := cols["Artist Name(s)"]
	albumCol, hasAlbum := cols["Album Name"]
	durCol, hasDur := cols["Duration (ms)"]
	coverCol, hasCover := cols[CoverColumn]
	if !hasCover {
		header = append(header, CoverColumn)
		records[0] = header
		coverCol = len(header) - 1
		cols[CoverColumn] = coverCol
		hasCover = true
	}

	for i := 1; i < len(records); i++ {
		row := records[i]
		if hasURI && track.URI != "" && field(row, uriCol) == track.URI {
			return nil
		}
		if hasTitle && hasArtist && strings.EqualFold(field(row, titleCol), track.Title) && strings.EqualFold(field(row, artistCol), track.Artists) {
			return nil
		}
	}

	newRow := make([]string, len(header))
	if hasURI {
		newRow[uriCol] = track.URI
	}
	if hasTitle {
		newRow[titleCol] = track.Title
	}
	if hasAlbum {
		newRow[albumCol] = track.Album
	}
	if hasArtist {
		newRow[artistCol] = track.Artists
	}
	if hasDur && track.DurationMs > 0 {
		newRow[durCol] = strconv.Itoa(track.DurationMs)
	}
	if hasCover && track.Cover != "" {
		newRow[coverCol] = track.Cover
	}
	records = append(records, newRow)

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.WriteAll(records); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, buf.Bytes(), 0o644)
}

// RemoveTrack removes any track matching trackURI or title/artist from path.
func RemoveTrack(path string, trackURI, title, artist string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	records, err := r.ReadAll()
	if err != nil {
		return err
	}
	if len(records) <= 1 {
		return nil
	}
	header := records[0]
	cols := map[string]int{}
	for i, name := range header {
		cols[strings.TrimPrefix(strings.TrimSpace(name), "\uFEFF")] = i
	}
	uriCol, hasURI := cols["Track URI"]
	titleCol, hasTitle := cols["Track Name"]
	artistCol, hasArtist := cols["Artist Name(s)"]

	out := records[:1]
	for i := 1; i < len(records); i++ {
		row := records[i]
		match := false
		if hasURI && trackURI != "" && field(row, uriCol) == trackURI {
			match = true
		} else if hasTitle && strings.EqualFold(field(row, titleCol), title) {
			if artist == "" || !hasArtist || strings.EqualFold(field(row, artistCol), artist) {
				match = true
			}
		}
		if !match {
			out = append(out, row)
		}
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.WriteAll(out); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, buf.Bytes(), 0o644)
}

// WritePlaylist writes a full playlist to path with standard Spotify export columns.
func WritePlaylist(path string, tracks []Track) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	header := []string{"Track URI", "Track Name", "Album Name", "Artist Name(s)", "Release Date", "Duration (ms)", CoverColumn}
	records := make([][]string, 0, len(tracks)+1)
	records = append(records, header)
	for _, t := range tracks {
		row := []string{
			t.URI,
			t.Title,
			t.Album,
			t.Artists,
			"",
			strconv.Itoa(t.DurationMs),
			t.Cover,
		}
		records = append(records, row)
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.WriteAll(records); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, buf.Bytes(), 0o644)
}

// ArtistRecord is an artist saved to the local library.
type ArtistRecord struct {
	URI        string `json:"uri"`
	Name       string `json:"name"`
	Thumbnail  string `json:"thumbnail"`
	FollowedAt string `json:"followedAt"`
}

// ReadArtists reads followed artists from path.
func ReadArtists(path string) ([]ArtistRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	records, err := r.ReadAll()
	if err != nil || len(records) <= 1 {
		return nil, err
	}
	header := records[0]
	cols := map[string]int{}
	for i, name := range header {
		cols[strings.TrimPrefix(strings.TrimSpace(name), "\uFEFF")] = i
	}
	uriCol, hasURI := cols["Artist URI"]
	nameCol, hasName := cols["Artist Name"]
	thumbCol, hasThumb := cols["Thumbnail URL"]
	timeCol, hasTime := cols["Followed At"]

	var artists []ArtistRecord
	for i := 1; i < len(records); i++ {
		row := records[i]
		name := ""
		if hasName {
			name = field(row, nameCol)
		}
		if name == "" {
			continue
		}
		var a ArtistRecord
		a.Name = name
		if hasURI {
			a.URI = field(row, uriCol)
		}
		if hasThumb {
			a.Thumbnail = field(row, thumbCol)
		}
		if hasTime {
			a.FollowedAt = field(row, timeCol)
		}
		artists = append(artists, a)
	}
	return artists, nil
}

// AppendArtist appends an artist to path.
func AppendArtist(path string, artist ArtistRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	existing, _ := ReadArtists(path)
	for _, a := range existing {
		if (artist.URI != "" && a.URI == artist.URI) || strings.EqualFold(a.Name, artist.Name) {
			return nil
		}
	}
	existing = append(existing, artist)
	return writeArtists(path, existing)
}

// RemoveArtist removes an artist from path.
func RemoveArtist(path string, name, uri string) error {
	existing, err := ReadArtists(path)
	if err != nil || len(existing) == 0 {
		return err
	}
	out := existing[:0]
	for _, a := range existing {
		match := (uri != "" && a.URI == uri) || strings.EqualFold(a.Name, name)
		if !match {
			out = append(out, a)
		}
	}
	return writeArtists(path, out)
}

func writeArtists(path string, artists []ArtistRecord) error {
	header := []string{"Artist URI", "Artist Name", "Thumbnail URL", "Followed At"}
	records := make([][]string, 0, len(artists)+1)
	records = append(records, header)
	for _, a := range artists {
		records = append(records, []string{a.URI, a.Name, a.Thumbnail, a.FollowedAt})
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.WriteAll(records); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, buf.Bytes(), 0o644)
}

func parseFile(path string) ([]Track, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return parse(file)
}

func parse(r io.Reader) ([]Track, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	header, err := reader.Read()
	if err != nil {
		return nil, err
	}
	cols := map[string]int{}
	for i, name := range header {
		cols[strings.TrimPrefix(strings.TrimSpace(name), "\uFEFF")] = i
	}
	titleCol, titleOK := cols["Track Name"]
	artistCol, artistOK := cols["Artist Name(s)"]
	if !titleOK || !artistOK {
		return nil, errors.New("not a Spotify playlist export")
	}
	uriCol, uriOK := cols["Track URI"]
	albumCol, albumOK := cols["Album Name"]
	durationCol, durationOK := cols["Duration (ms)"]
	var tracks []Track
	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return tracks, nil
		}
		if err != nil {
			return nil, err
		}
		title := field(row, titleCol)
		if title == "" {
			continue
		}
		track := Track{Title: title, Artists: field(row, artistCol)}
		if uriOK {
			track.URI = field(row, uriCol)
		}
		if albumOK {
			track.Album = field(row, albumCol)
		}
		if durationOK {
			track.DurationMs, _ = strconv.Atoi(field(row, durationCol))
		}
		if coverCol, ok := cols[CoverColumn]; ok {
			track.Cover = field(row, coverCol)
		}
		tracks = append(tracks, track)
	}
}

func field(row []string, index int) string {
	if index < 0 || index >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[index])
}

// FirstArtist is the first name in a Spotify artist list, which separates
// collaborators with semicolons.
func FirstArtist(artists string) string {
	artists = strings.TrimSpace(artists)
	if i := strings.Index(artists, ";"); i >= 0 {
		artists = artists[:i]
	}
	return strings.TrimSpace(artists)
}

// Query is the YouTube Music search for a track.
func Query(track Track) string {
	artist := FirstArtist(track.Artists)
	if artist == "" {
		return track.Title
	}
	return artist + " " + track.Title
}

// FormatDuration renders a Spotify length the way the player shows one.
func FormatDuration(ms int) string {
	if ms <= 0 {
		return ""
	}
	seconds := ms / 1000
	if seconds >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, (seconds%3600)/60, seconds%60)
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

// ParseClock reads a length like 3:24 or 1:02:03.
func ParseClock(value string) time.Duration {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}
	n := make([]int, len(parts))
	for i, part := range parts {
		v, err := strconv.Atoi(part)
		if err != nil {
			return 0
		}
		n[i] = v
	}
	seconds := n[len(n)-1] + n[len(n)-2]*60
	if len(n) == 3 {
		seconds += n[0] * 3600
	}
	return time.Duration(seconds) * time.Second
}

// Pick chooses the candidate that best matches track. The bool is false when
// none of them is close enough to play.
func Pick(track Track, candidates []Candidate) (int, bool) {
	best, score := -1, 0
	for i, candidate := range candidates {
		got := scoreMatch(track, candidate)
		if got > score {
			best, score = i, got
		}
	}
	if best < 0 || score < 60 {
		return -1, false
	}
	return best, true
}

func scoreMatch(track Track, candidate Candidate) int {
	if !candidate.Song {
		return 0
	}
	want, got := normalize(track.Title), normalize(candidate.Title)
	if want == "" || got == "" {
		return 0
	}
	score := 0
	switch {
	case want == got:
		score += 60
	case strings.Contains(got, want) || strings.Contains(want, got):
		score += 40
	default:
		return 0
	}
	if artistMatches(track, candidate) {
		score += 30
	}
	wantLen := time.Duration(track.DurationMs) * time.Millisecond
	gotLen := ParseClock(candidate.Duration)
	if wantLen > 0 && gotLen > 0 {
		delta := wantLen - gotLen
		if delta < 0 {
			delta = -delta
		}
		switch {
		case delta <= 3*time.Second:
			score += 20
		case delta <= 12*time.Second:
			score += 8
		case delta > 45*time.Second:
			score -= 25
		}
	}
	if candidate.Video {
		score -= 20
	}
	return score
}

func artistMatches(track Track, candidate Candidate) bool {
	want := artistTokens(track.Artists)
	if len(want) == 0 {
		return false
	}
	got := normalize(candidate.Artist + " " + candidate.Artists)
	for _, token := range want {
		if token != "" && strings.Contains(got, token) {
			return true
		}
	}
	return false
}

func artistTokens(artists string) []string {
	artists = strings.NewReplacer(";", " ", ",", " ", "&", " ").Replace(artists)
	return strings.Fields(normalize(artists))
}

func normalize(value string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}
