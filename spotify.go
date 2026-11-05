package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/elianiva/meiro/internal/spotifyimport"
	"github.com/elianiva/meiro/m3"
	"github.com/elianiva/meiro/youtube"
)

// defaultSpotifyFolder is the export this copy of Meiro was asked to play.
const defaultSpotifyFolder = "/Users/uros/Downloads/spotify_playlists"

// spotifyState is the local library of Spotify playlists and a match that is
// still looking songs up on YouTube Music.
type spotifyState struct {
	file   string
	folder string
	// home is Meiro's own copy of the playlist files. Covers are written
	// there, not back to the folder they were imported from.
	home      string
	lib       spotifyimport.Library
	ready     bool
	query     string
	songQuery string
	// playlists and tracks are separate lists. The router draws the page
	// being left and the page being opened in the same frame, and one
	// ListState cannot be shown twice.
	playlists *ui.ListState
	tracks    *ui.ListState
	// trackListFor is the playlist the track list was built for.
	trackListFor string
	// autoTried stops an empty library from importing the default folder
	// on every visit.
	autoTried bool
	importing task
	saving    task
	dirty     bool
	job       task
	// session identifies the play that is resolving, so a newer play ignores
	// matches from the one it replaced.
	session   int
	resolving bool
	// finding is set only until the song that was pressed has a match.
	// Matching the rest of the playlist continues quietly after that.
	finding    bool
	waiting    bool
	playlistID string
}

func (a *app) openSpotifyLibrary(file string) {
	a.spotify.file = file
	a.spotify.home = filepath.Join(filepath.Dir(file), "spotify")
	a.spotify.lib = spotifyimport.Load(file)
	a.keepSpotifyAtHome()
	a.spotify.lib.ReadCovers()
	a.spotify.ready = true
	if a.spotify.lib.Source != "" {
		a.spotify.folder = a.spotify.lib.Source
	} else {
		a.spotify.folder = defaultSpotifyFolder
	}
}

// keepSpotifyAtHome copies an imported folder into Meiro's own directory once.
// After that, reading and writing covers stays inside Meiro.
func (a *app) keepSpotifyAtHome() {
	source := filepath.Clean(a.spotify.lib.Source)
	home := a.spotify.home
	if source == "" || source == "." || home == "" || source == filepath.Clean(home) {
		return
	}
	if err := spotifyimport.CopyCSVs(source, home); err != nil {
		log.Printf("spotify library: %v", err)
		return
	}
	a.spotify.lib.Source = home
	a.saveSpotify()
}

func (a *app) ensureSpotify() {
	if a.spotify.ready {
		return
	}
	a.spotify.ready = true
	if a.spotify.folder == "" {
		a.spotify.folder = defaultSpotifyFolder
	}
}

// openSpotify runs when the Spotify page is shown. The first visit imports
// the default folder when the library is still empty.
func (a *app) openSpotify() {
	a.ensureSpotify()
	if len(a.spotify.lib.Playlists) > 0 || a.spotify.autoTried || a.spotify.importing.busy() {
		return
	}
	a.spotify.autoTried = true
	info, err := os.Stat(a.spotify.folder)
	if err != nil || !info.IsDir() {
		return
	}
	a.importSpotifyFolder(a.spotify.folder)
}

func (a *app) openSpotifyPlaylist() {
	a.ensureSpotify()
	_, id := a.route()
	if a.spotify.trackListFor == id {
		return
	}
	a.spotify.trackListFor = id
	a.spotify.tracks = &ui.ListState{}
	a.spotify.songQuery = ""
}

func (a *app) importSpotifyFolder(dir string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return
	}
	previous := a.spotify.lib
	home := a.spotify.home
	startTask(a, &a.spotify.importing, noTimeout, func(context.Context) (spotifyimport.Library, error) {
		dest := dir
		if home != "" && filepath.Clean(home) != filepath.Clean(dir) {
			if err := spotifyimport.CopyCSVs(dir, home); err != nil {
				return spotifyimport.Library{}, err
			}
			dest = home
		}
		lib, _, err := spotifyimport.ImportDir(dest, previous)
		return lib, err
	}, func(lib spotifyimport.Library, err error) {
		if err != nil {
			a.notice = "Could not import playlists: " + firstLine(err.Error())
			return
		}
		a.spotify.lib = lib
		a.spotify.folder = lib.Source
		a.saveSpotify()
		a.notice = fmt.Sprintf("Imported %d playlists", len(lib.Playlists))
	})
}

func (a *app) saveSpotify() {
	if a.spotify.file == "" {
		return
	}
	a.spotify.dirty = true
	if a.spotify.saving.busy() {
		return
	}
	a.writeSpotify()
}

// writeSpotify saves the library. A change that lands while a save is in
// flight is written again when that save finishes, so an older copy cannot
// replace a newer one.
func (a *app) writeSpotify() {
	if !a.spotify.dirty || a.spotify.file == "" || a.run == nil {
		return
	}
	a.spotify.dirty = false
	lib := spotifyimport.Clone(a.spotify.lib)
	path := a.spotify.file
	startTask(a, &a.spotify.saving, noTimeout, func(context.Context) (struct{}, error) {
		return struct{}{}, spotifyimport.Save(path, lib)
	}, func(_ struct{}, err error) {
		if err != nil {
			log.Printf("spotify library: %v", err)
		}
		if a.spotify.dirty {
			a.writeSpotify()
		}
	})
}

// playSpotify plays the playlist from index. The chosen song is looked up on
// YouTube Music and starts as soon as it is found; the songs after it are
// matched while it plays.
func (a *app) playSpotify(playlistID string, index int) {
	playlist, ok := a.spotify.lib.PlaylistByID(playlistID)
	if !ok || index < 0 || index >= len(playlist.Tracks) {
		return
	}
	a.stopSpotifyResolve()
	session := a.spotify.session
	a.spotify.resolving = true
	a.spotify.finding = true
	a.spotify.playlistID = playlistID
	client := a.client()
	tracks := append([]spotifyimport.Track(nil), playlist.Tracks[index:]...)
	name := playlist.Name
	startProgress(a, &a.spotify.job, noTimeout, func(ctx context.Context, progress func(func())) (int, error) {
		misses := 0
		started := false
		for i, track := range tracks {
			if ctx.Err() != nil {
				return misses, ctx.Err()
			}
			item, err := a.matchSpotifyTrack(ctx, client, track)
			success := err == nil && item.VideoID != ""
			if !success {
				misses++
			}
			first := success && !started
			if success {
				started = true
			}
			at := index + i
			title := track.Title
			progress(func() {
				if a.spotify.session != session {
					return
				}
				if !success {
					if i == 0 {
						a.notice = "Couldn't find “" + title + "” on YouTube Music"
					}
					return
				}
				a.spotify.lib.SetMatch(playlistID, at, item.VideoID, item.Thumbnail)
				a.writeSpotifyCover(playlistID, at, item.Thumbnail)
				if first {
					a.spotify.finding = false
					a.startResolved(item, name)
					return
				}
				a.queue.appendPlaylist([]youtube.MusicItem{item}, a.shuffle)
				if a.spotify.waiting {
					a.spotify.waiting = false
					a.advance()
				}
			})
		}
		return misses, nil
	}, func(misses int, err error) {
		if a.spotify.session != session {
			return
		}
		a.spotify.resolving = false
		a.spotify.finding = false
		wasWaiting := a.spotify.waiting
		a.spotify.waiting = false
		if err != nil && !errors.Is(err, context.Canceled) {
			a.notice = "Couldn't match songs: " + firstLine(err.Error())
		} else if misses > 1 {
			a.notice = fmt.Sprintf("Couldn't find %d songs on YouTube Music", misses)
		}
		if wasWaiting && !a.player.Playing() {
			a.advance()
		}
		a.saveSpotify()
	})
}

func (a *app) stopSpotifyResolve() {
	saving := a.spotify.resolving
	a.spotify.job.stop()
	a.spotify.resolving = false
	a.spotify.finding = false
	a.spotify.waiting = false
	a.spotify.session++
	if saving {
		a.saveSpotify()
	}
}

// startResolved plays a song that was just matched, without cancelling the
// match still walking the rest of the playlist.
func (a *app) startResolved(item youtube.MusicItem, source string) {
	a.queueRest.stop()
	a.clearQueueSelection()
	a.queue.replace(item, []youtube.MusicItem{item}, 0, source)
	if a.settings.ExcludeAIGenerated {
		a.queue.dropMarkedAI(a.excludesItem)
	}
	clear(a.scrobble.queued)
	a.learnCredits(item)
	a.resetUpNext(youtube.UpNextOptions{VideoID: item.VideoID})
	a.start(item)
}

func (a *app) writeSpotifyCover(playlistID string, index int, cover string) {
	cover = strings.TrimSpace(cover)
	if cover == "" || a.run == nil {
		return
	}
	playlist, ok := a.spotify.lib.PlaylistByID(playlistID)
	if !ok || index < 0 || index >= len(playlist.Tracks) {
		return
	}
	uri := playlist.Tracks[index].URI
	path := filepath.Join(a.spotify.lib.Source, playlist.File)
	if uri == "" || a.spotify.lib.Source == "" || playlist.File == "" {
		return
	}
	a.run(func() {
		if err := spotifyimport.WriteCoverURL(path, uri, cover); err != nil {
			log.Printf("spotify cover: %v", err)
		}
	})
}

func (a *app) matchSpotifyTrack(ctx context.Context, client *youtube.Client, track spotifyimport.Track) (youtube.MusicItem, error) {
	if track.VideoID != "" && strings.TrimSpace(track.Cover) != "" {
		return musicItemFromSpotify(track, track.VideoID), nil
	}
	if track.VideoID != "" && client == nil {
		return musicItemFromSpotify(track, track.VideoID), nil
	}
	if client == nil {
		return youtube.MusicItem{}, errors.New("YouTube Music is not available")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result, err := client.Search(ctx, spotifyimport.Query(track), youtube.SearchOptions{})
	if track.VideoID != "" {
		item := musicItemFromSpotify(track, track.VideoID)
		if err == nil {
			if thumb := thumbnailForVideo(result, track.VideoID); thumb != "" {
				item.Thumbnail = thumb
			}
		}
		if item.Thumbnail == "" && err == nil {
			if params := songsFilter(result); params != "" {
				narrowed, err := client.Search(ctx, spotifyimport.Query(track), youtube.SearchOptions{Params: params})
				if err == nil {
					if thumb := thumbnailForVideo(narrowed, track.VideoID); thumb != "" {
						item.Thumbnail = thumb
					}
				}
			}
		}
		return item, nil
	}
	if err != nil {
		return youtube.MusicItem{}, err
	}
	if item, ok := pickSpotify(track, result); ok && !item.IsVideo() {
		return item, nil
	}
	fallback, haveFallback := pickSpotify(track, result)
	if params := songsFilter(result); params != "" {
		narrowed, err := client.Search(ctx, spotifyimport.Query(track), youtube.SearchOptions{Params: params})
		if err == nil {
			if item, ok := pickSpotify(track, narrowed); ok {
				return keepThumbnail(item, track.Cover), nil
			}
		}
	}
	if haveFallback {
		return keepThumbnail(fallback, track.Cover), nil
	}
	return youtube.MusicItem{}, errors.New("no close match")
}

func thumbnailForVideo(result *youtube.SearchResult, videoID string) string {
	if result == nil || videoID == "" {
		return ""
	}
	if result.TopResult.VideoID == videoID && result.TopResult.Thumbnail != "" {
		return result.TopResult.Thumbnail
	}
	for _, item := range result.Items {
		if item.VideoID == videoID && item.Thumbnail != "" {
			return item.Thumbnail
		}
	}
	return ""
}

func keepThumbnail(item youtube.MusicItem, cover string) youtube.MusicItem {
	if item.Thumbnail == "" {
		item.Thumbnail = cover
	}
	return item
}

func songsFilter(result *youtube.SearchResult) string {
	if result == nil {
		return ""
	}
	for _, filter := range result.Filters {
		if strings.EqualFold(filter.Label, "Songs") && filter.Params != "" {
			return filter.Params
		}
	}
	return ""
}

func pickSpotify(track spotifyimport.Track, result *youtube.SearchResult) (youtube.MusicItem, bool) {
	if result == nil {
		return youtube.MusicItem{}, false
	}
	var items []youtube.MusicItem
	add := func(item youtube.MusicItem) {
		if item.VideoID == "" || !item.IsTrack() {
			return
		}
		items = append(items, item)
	}
	add(result.TopResult)
	for _, item := range result.Items {
		add(item)
	}
	candidates := make([]spotifyimport.Candidate, len(items))
	for i, item := range items {
		candidates[i] = spotifyimport.Candidate{
			Title: item.Title, Artist: item.Artist, Artists: item.Artists,
			Duration: item.Duration, Video: item.IsVideo(), Song: true,
		}
	}
	index, ok := spotifyimport.Pick(track, candidates)
	if !ok {
		return youtube.MusicItem{}, false
	}
	chosen := items[index]
	chosen.Artist = firstNonEmpty(chosen.Artist, spotifyimport.FirstArtist(track.Artists))
	if chosen.Artists == "" {
		chosen.Artists = strings.ReplaceAll(track.Artists, ";", ", ")
	}
	if chosen.Album == "" {
		chosen.Album = track.Album
	}
	return chosen, true
}

func musicItemFromSpotify(track spotifyimport.Track, videoID string) youtube.MusicItem {
	artist := spotifyimport.FirstArtist(track.Artists)
	artists := strings.ReplaceAll(track.Artists, ";", ", ")
	subtitle := artist
	if track.Album != "" {
		if subtitle != "" {
			subtitle += " • "
		}
		subtitle += track.Album
	}
	return youtube.MusicItem{
		ID: track.URI, VideoID: videoID, Title: track.Title,
		Artist: artist, Artists: artists, Album: track.Album,
		Duration:  spotifyimport.FormatDuration(track.DurationMs),
		Thumbnail: track.Cover,
		Kind:      youtube.KindTrack, Subtitle: subtitle,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (a *app) spotifyPage(c *ui.Context) {
	sc := m3.Of(c).Scheme
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Shrink(0).Padding(8, pageGutter, 0).Gap(12).AlignItems(ui.Center).Children(func() {
			m3.SearchBar(c, m3.SearchSpec{
				Value: &a.spotify.folder, Placeholder: "Folder of Spotify CSV files",
				Key: "spotify-folder", Height: 48,
			})
			if m3.Button(c, m3.ButtonSpec{
				Label: "Import", Kind: m3.Filled, Size: m3.Small40, Key: "spotify-import",
				Disabled: strings.TrimSpace(a.spotify.folder) == "" || a.spotify.importing.busy(),
			}).Clicked() {
				a.importSpotifyFolder(a.spotify.folder)
			}
		})
		note := fmt.Sprintf("%d playlists on this computer. Play looks each song up on YouTube Music.", len(a.spotify.lib.Playlists))
		if a.spotify.importing.busy() {
			note = "Importing playlists…"
		}
		m3.BodyMedium.Style(ui.Text(c, note), false).Shrink(0).Padding(8, pageGutter, 4).TextColor(sc.OnSurfaceVariant)
		ui.Row(c).Shrink(0).Height(48).Padding(0, pageGutter, 8).Children(func() {
			m3.SearchBar(c, m3.SearchSpec{
				Value: &a.spotify.query, Placeholder: "Search playlists",
				Key: "spotify-playlist-filter", Height: 48,
			})
		})
		playlists := a.filteredSpotifyPlaylists()
		if len(playlists) == 0 && len(a.spotify.lib.Playlists) > 0 {
			a.message(c, m3.IconAlbum, "Nothing matches", "Try another search.", "", nil)
			return
		}
		if len(a.spotify.lib.Playlists) == 0 && !a.spotify.importing.busy() {
			a.message(c, m3.IconAlbum, "No playlists yet", "Choose the folder of CSV files exported from Spotify, then import them.", "", nil)
			return
		}
		a.spotifyList(c, &a.spotify.playlists, len(playlists), func(i int) any { return playlists[i].ID }, func(i int) string { return playlists[i].Name }, func(i int) {
			a.spotifyPlaylistRow(c, playlists[i])
		})
	})
}

func (a *app) filteredSpotifyPlaylists() []spotifyimport.Playlist {
	query := strings.ToLower(strings.TrimSpace(a.spotify.query))
	if query == "" {
		return a.spotify.lib.Playlists
	}
	var matched []spotifyimport.Playlist
	for _, playlist := range a.spotify.lib.Playlists {
		if strings.Contains(strings.ToLower(playlist.Name), query) {
			matched = append(matched, playlist)
		}
	}
	return matched
}

func (a *app) spotifyPlaylistPage(c *ui.Context) {
	_, id := a.route()
	playlist, ok := a.spotify.lib.PlaylistByID(id)
	if !ok {
		ui.Column(c).Fill().Center().Children(func() {
			a.message(c, m3.IconAlbum, "Playlist not found", "It is not in the imported library.", "Back", func() { a.router.Back() })
		})
		return
	}
	sc := m3.Of(c).Scheme
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Shrink(0).Padding(8, pageGutter, 0).Gap(8).AlignItems(ui.Center).Children(func() {
			if m3.IconButton(c, m3.IconButtonSpec{Icon: m3.IconArrowBack, Label: "Back", Key: "spotify-back"}).Clicked() {
				a.router.Back()
			}
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				m3.EmphasizedText(c, m3.HeadlineSmall, playlist.Name).SingleLine()
				subtitle := fmt.Sprintf("%d songs", len(playlist.Tracks))
				if a.spotify.finding && a.spotify.playlistID == id {
					subtitle = "Matching with YouTube Music…"
				}
				m3.BodyMedium.Style(ui.Text(c, subtitle), false).TextColor(sc.OnSurfaceVariant).SingleLine()
			})
			if m3.Button(c, m3.ButtonSpec{
				Label: "Play", Icon: m3.IconPlay, Kind: m3.Filled, Size: m3.Small40, Key: "spotify-play-all",
				Disabled: len(playlist.Tracks) == 0,
			}).Clicked() {
				a.playSpotify(id, 0)
			}
		})
		ui.Row(c).Shrink(0).Height(48).Padding(0, pageGutter, 8).Children(func() {
			m3.SearchBar(c, m3.SearchSpec{
				Value: &a.spotify.songQuery, Placeholder: "Search in this playlist",
				Key: "spotify-song-filter", Height: 48,
			})
		})
		rows := filteredSpotifyTracks(playlist.Tracks, a.spotify.songQuery)
		if len(rows) == 0 {
			a.message(c, m3.IconAlbum, "Nothing matches", "Try another search.", "", nil)
			return
		}
		a.spotifyList(c, &a.spotify.tracks, len(rows), func(i int) any { return rows[i].index }, func(i int) string { return rows[i].track.Title }, func(i int) {
			a.spotifyTrackRow(c, id, rows[i])
		})
	})
}

type spotifyTrackRow struct {
	index int
	track spotifyimport.Track
}

func filteredSpotifyTracks(tracks []spotifyimport.Track, query string) []spotifyTrackRow {
	query = strings.ToLower(strings.TrimSpace(query))
	rows := make([]spotifyTrackRow, 0, len(tracks))
	for i, track := range tracks {
		if query != "" && !strings.Contains(strings.ToLower(track.Title+" "+track.Artists+" "+track.Album), query) {
			continue
		}
		rows = append(rows, spotifyTrackRow{index: i, track: track})
	}
	return rows
}

func (a *app) spotifyList(c *ui.Context, state **ui.ListState, n int, key func(int) any, label func(int) string, row func(int)) {
	if *state == nil {
		*state = &ui.ListState{}
	}
	list := *state
	list.Key = key
	list.Label = label
	ui.List(c, list, n, row).Grow(1).Padding(0, 0, a.clearance())
}

func (a *app) spotifyPlaylistRow(c *ui.Context, playlist spotifyimport.Playlist) {
	sc := m3.Of(c).Scheme
	key := "spotify-pl-" + playlist.ID
	row := ui.Row(c.Key(key)).Height(rowHeight).Shrink(0).Padding(8).Gap(8).AlignItems(ui.Center).Margin(0, pageGutter-8).Radius(m3.Large)
	var open, play ui.Element
	row.Children(func() {
		open = ui.ButtonBase(c.Key(key + "-open")).Grow(1).MinWidth(0).Height(rowHeight - 16).AlignItems(ui.Center).
			Radius(m3.Large).Cursor(ui.CursorPointer).Label(playlist.Name).Background(ui.Transparent)
		open.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				m3.BodyLarge.Style(ui.Text(c, playlist.Name), false).SingleLine()
				m3.BodyMedium.Style(ui.Text(c, fmt.Sprintf("%d songs", len(playlist.Tracks))), false).SingleLine().TextColor(sc.OnSurfaceVariant)
			})
		})
		play = m3.IconButton(c, m3.IconButtonSpec{Icon: m3.IconPlay, Label: "Play " + playlist.Name, Key: key + "-play"})
	})
	row.Background(m3.StateFill(ui.Transparent, sc.OnSurface, row.Hovered(), open.Pressed(), false))
	if open.Clicked() {
		a.router.Push(routeByID(routeSpotifyPlaylist).path(playlist.ID))
	}
	if play.Clicked() {
		a.playSpotify(playlist.ID, 0)
	}
}

func (a *app) spotifyTrackRow(c *ui.Context, playlistID string, row spotifyTrackRow) {
	sc := m3.Of(c).Scheme
	track := row.track
	key := fmt.Sprintf("spotify-tr-%s-%d", playlistID, row.index)
	playing := track.VideoID != "" && track.VideoID == a.current.VideoID
	line := ui.Row(c.Key(key)).Height(rowHeight).Shrink(0).Padding(8).Gap(12).AlignItems(ui.Center).Margin(0, pageGutter-8).Radius(m3.Large)
	var main ui.Element
	line.Children(func() {
		main = ui.ButtonBase(c.Key(key + "-play")).Grow(1).MinWidth(0).Height(rowHeight - 16).Gap(12).AlignItems(ui.Center).
			Radius(m3.Large).Cursor(ui.CursorPointer).Label(track.Title).Background(ui.Transparent)
		main.Children(func() {
			title, sub := sc.OnSurface, sc.OnSurfaceVariant
			if playing {
				title, sub = sc.OnSecondaryContainer, sc.OnSecondaryContainer.Alpha(0.8)
			}
			// A cover already stored in the CSV is drawn here. Songs without one
			// stay blank until they are played and matched.
			if track.Cover != "" && a.thumbs != nil {
				a.art(c, track.Cover, 128, 48, 4).Shrink(0)
			}
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				m3.BodyLarge.Style(ui.Text(c, track.Title), false).SingleLine().TextColor(title)
				detail := spotifyimport.FirstArtist(track.Artists)
				if track.Album != "" {
					if detail != "" {
						detail += " • "
					}
					detail += track.Album
				}
				m3.BodyMedium.Style(ui.Text(c, detail), false).SingleLine().TextColor(sub)
			})
			if length := spotifyimport.FormatDuration(track.DurationMs); length != "" {
				m3.BodyMedium.Style(ui.Text(c, length), false).TextColor(sub).Shrink(0)
			}
		})
	})
	fill := ui.Transparent
	if playing {
		fill = sc.SecondaryContainer
	}
	line.Background(m3.StateFill(fill, sc.OnSurface, line.Hovered() && !playing, main.Pressed(), false))
	if main.Clicked() {
		a.playSpotify(playlistID, row.index)
	}
}
