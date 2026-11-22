package app

import (
	"fmt"
	"image"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/i18n"
	"mygo-music/internal/local"
	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
	"mygo-music/internal/sharecard"
)

// framesDir is where MM_FRAMES says to save a look at the views, which a test
// skips without.
func framesDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("MM_FRAMES")
	if dir == "" {
		t.Skip()
	}
	return dir
}

// With MM_FRAMES set, saves the share editor at desktop and compact sizes.
func TestShareEditorFrame(t *testing.T) {
	dir := framesDir(t)
	a := newTestApp()
	a.settings.LyricsScript = "simplified"
	track := music.Track{ID: "share-frame", Source: "fake", Name: "北方的夜 · Northern Lights", Artists: []music.Artist{{Name: "The Polar Lights"}}, URL: "https://example.com/song"}
	cover := coverImage(ui.RGB(28, 94, 132), ui.RGB(214, 162, 78))
	lines := lyrics.Build("[00:01]讓風慢慢經過\n[00:04]你會聽見遠方\n[00:08]我們一起出發", "[00:01]Let the evening wind pass softly.\n[00:04]You'll hear the distance.\n[00:08]Let's set out together.")
	selected := map[int]bool{0: true, 2: true}
	png, err := sharecard.Render(sharecard.Card{Title: track.Name, Artist: track.ArtistNames(), URL: track.URL, Cover: cover,
		Lines: sharecard.Select(lyrics.Display(lines, lyrics.DisplayOptions{Simplify: true}), selected, true)})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := ui.DecodeBitmap(png)
	if err != nil {
		t.Fatal(err)
	}
	a.sharing = &shareDraft{open: true, track: track, lines: lines, selected: selected, cover: cover, preview: preview, png: png}
	for _, size := range []struct {
		width, height int
		name          string
	}{{1200, 800, "share-editor.png"}, {760, 640, "share-editor-compact.png"}} {
		tester := ui.NewTester(a.view, size.width, size.height)
		settled(tester)
		saveFrame(t, dir, size.name, tester.Image())
	}
}

// With MM_FRAMES set, saves Home as it greets a listener with no music yet.
func TestHomeEmptyFrame(t *testing.T) {
	dir := framesDir(t)
	a := localApp(t)
	tt := ui.NewTester(a.view, 1180, 780)
	settled(tt)
	saveFrame(t, dir, "home-empty.png", tt.Image())
}

// With MM_FRAMES set, saves Home showing cached playlists while account lookup waits.
func TestHomeCachedWhileAccountLoadsFrame(t *testing.T) {
	dir := framesDir(t)
	a := newTestApp()
	lib := a.library
	lib.started, lib.homeLoading, lib.accountAsked = true, false, false
	lib.home = nil
	lib.mine = []music.Playlist{{ID: "cached", Name: "离线缓存歌单", Mine: true}}
	a.mineCollapsed = true
	blocked := make(chan struct{})
	defer close(blocked)
	fake(a).account = func() (*music.Account, error) {
		<-blocked
		return nil, nil
	}
	a.loadAccount(lib)
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	saveFrame(t, dir, "home-cached-account-loading.png", tt.Image())
}

// framesTracks are songs with covers, by an invented band.
func framesTracks(a *app) []music.Track {
	covers := []struct{ from, to ui.Color }{
		{ui.RGB(40, 90, 170), ui.RGB(220, 120, 160)},
		{ui.RGB(230, 150, 70), ui.RGB(120, 40, 90)},
		{ui.RGB(30, 130, 110), ui.RGB(210, 220, 140)},
	}
	band := music.Artist{ID: "1", Name: "The Polar Lights"}
	var tracks []music.Track
	for i, name := range []string{"Northern Lights", "Slow Tide", "A Long Way North", "Paper Boats", "The Harbor at Night", "Quiet Hours", "Last Ferry Home", "Snowfield"} {
		url := fmt.Sprintf("cover:%d", i%len(covers))
		giveCover(a, url, coverImage(covers[i%len(covers)].from, covers[i%len(covers)].to))
		tracks = append(tracks, music.Track{
			ID: fmt.Sprint(i + 1), Source: "fake", Name: name, Artists: []music.Artist{band},
			Album:    music.Album{ID: fmt.Sprint(10 + i%len(covers)), Name: "Polar Night", ImageURL: url},
			Duration: int64(180000 + 21000*i),
		})
	}
	return tracks
}

// 设置 MM_FRAMES 后保存歌单及专辑入口的明暗主题与悬停效果，供目视验收。
func TestPlaylistFrame(t *testing.T) {
	dir := framesDir(t)
	language := i18n.Language()
	i18n.SetLanguage(i18n.Chinese)
	t.Cleanup(func() { i18n.SetLanguage(language) })
	a := newTestApp()
	a.account = &music.Account{ID: "1", Name: "Alex"}
	tracks := framesTracks(a)
	a.mine = []music.Playlist{
		{ID: "10", Name: "Evening Walks", ImageURL: "cover:0", Mine: true},
		{ID: "11", Name: "Focus", ImageURL: "cover:1", Mine: true},
	}
	a.saved = []music.Playlist{{ID: "20", Name: "Staff Picks", ImageURL: "cover:2", Saved: true}}
	a.playlists["10"] = &music.Playlist{ID: "10", Name: "Evening Walks", Owner: "Alex", Description: "For the long way home.", ImageURL: "cover:0", Mine: true, Tracks: tracks}
	a.route = route{page: pagePlaylist, src: "fake", id: "10"}
	tt := ui.NewTester(a.view, 1180, 780)
	tt.SetDark(false)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	settled(tt)
	saveFrame(t, dir, "playlist.png", tt.Image())
	album, ok := tt.Find("Polar Night")
	if !ok {
		t.Fatal("no album link")
	}
	tt.Move(album.X+album.W/2, album.Y+album.H/2)
	settled(tt)
	saveFrame(t, dir, "playlist-album-hover.png", tt.Image())
	tt.SetDark(true)
	settled(tt)
	saveFrame(t, dir, "playlist-album-hover-dark.png", tt.Image())
	tt.Move(0, 0)
	settled(tt)
	saveFrame(t, dir, "playlist-dark.png", tt.Image())
}

// With MM_FRAMES set, saves Now Playing with lyrics following a song.
func TestNowPlayingFrame(t *testing.T) {
	dir := framesDir(t)
	a := newTestApp()
	a.account = &music.Account{ID: "1", Name: "Alex"}
	tracks := framesTracks(a)
	a.pl.Cue(tracks)
	a.pl.SetLyrics(lyrics.Build(
		"[00:04.00]The sky goes green at the edges\n[00:09.00]And everyone stops to look\n[00:14.00]Nobody says a word\n[00:19.00]Nobody needs to\n[00:24.00]We stand out on the ice\n[00:29.00]Until the light goes home",
		"",
	))
	a.pl.Hold(10 * time.Second)
	a.nowPlaying, a.npPane = true, paneLyrics
	tt := ui.NewTester(a.view, 1180, 780)
	tt.SetDark(true)
	tt.Advance(2 * time.Second)
	tt.Frame()
	saveFrame(t, dir, "now-playing.png", tt.Image())
}

// With MM_FRAMES set, saves each rhythm style and its music-led interlude.
func TestStageStylesFrame(t *testing.T) {
	dir := framesDir(t)
	a, tester, _ := playingApp()
	a.libs[0].account = &music.Account{ID: "test", Name: "Headless"}
	track := a.pl.Current()
	track.Album.ImageURL = "stage-cover"
	giveCover(a, track.Album.ImageURL, coverImage(ui.RGB(25, 92, 132), ui.RGB(210, 164, 82)))
	a.nowPlaying, a.npPane = true, paneStage
	a.pl.Hold(2 * time.Second)
	tester.SetDark(true)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	settled(tester)
	for _, style := range stageStyles {
		a.settings.StageStyle = style.id
		tester.Frame()
		saveFrame(t, dir, "stage-"+map[string]string{"": "flow", "partita": "steps", "tilt": "tilt", "spotlight": "spotlight", "wave": "wave"}[style.id]+".png", tester.Image())
	}
	a.pl.Hold(7 * time.Second)
	for _, style := range []string{"spotlight", "wave"} {
		a.settings.StageStyle = style
		tester.Frame()
		saveFrame(t, dir, "interlude-"+style+".png", tester.Image())
	}
	a.pl.Hold(2 * time.Second)
	tester.SetPreferences(ui.Preferences{})
	for _, style := range []string{"spotlight", "wave"} {
		a.settings.StageStyle = style
		for _, intensity := range []string{"gentle", "standard", "lively"} {
			a.settings.StageIntensity = intensity
			tester.Frame()
			saveFrame(t, dir, "stage-"+style+"-"+intensity+".png", tester.Image())
		}
	}
}

// With MM_FRAMES set, saves Now Playing for a local song with no cover.
func TestNowPlayingNoCoverFrame(t *testing.T) {
	dir := framesDir(t)
	a := newTestApp()
	tracks := framesTracks(a)
	for i := range tracks {
		tracks[i].Album.ImageURL = ""
	}
	a.pl.Cue(tracks)
	a.pl.Hold(10 * time.Second)
	a.nowPlaying, a.npPane = true, paneQueue
	tt := ui.NewTester(a.view, 1180, 780)
	tt.SetDark(true)
	tt.Advance(2 * time.Second)
	tt.Frame()
	saveFrame(t, dir, "now-playing-no-cover.png", tt.Image())
}

// With MM_FRAMES set, saves Now Playing growing out of the player bar's
// cover, a frame every 50 ms, and shrinking back into it, in a sheet each.
func TestNowPlayingMotionFrames(t *testing.T) {
	dir := framesDir(t)
	a := newTestApp()
	a.pl.Cue(framesTracks(a))
	a.pl.SetLyrics(nil)
	a.pl.Hold(10 * time.Second)
	a.npPane = paneQueue
	tt := ui.NewTester(a.view, 1180, 780)
	tt.SetDark(true)
	settled(tt)
	sheet := func(name string) {
		var shots []image.Image
		for range 12 {
			shots = append(shots, tt.Image())
			tt.Advance(50 * time.Millisecond)
		}
		saveFrame(t, dir, name, contactSheet(shots, 4))
	}
	if err := tt.Click("Open Now Playing"); err != nil {
		t.Fatal(err)
	}
	sheet("np-open.png")
	settled(tt)
	a.nowPlaying = false
	tt.Frame()
	sheet("np-close.png")

	// Pulled down halfway, then let go to fall into the bar.
	a.nowPlaying = true
	settled(tt)
	tt.Press(450, 300)
	tt.Frame()
	for i := 1; i <= 10; i++ {
		tt.Advance(16 * time.Millisecond)
		tt.Move(450, 300+float32(i)*24)
		tt.Frame()
	}
	saveFrame(t, dir, "np-pulled.png", tt.Image())
	tt.Release(450, 540)
	tt.Frame()
	sheet("np-let-go.png")
}

// contactSheet lays shots out in rows of cols, each a third of its size.
func contactSheet(shots []image.Image, cols int) image.Image {
	b := shots[0].Bounds()
	cw, ch := b.Dx()/3, b.Dy()/3
	rows := (len(shots) + cols - 1) / cols
	out := image.NewRGBA(image.Rect(0, 0, cw*cols, ch*rows))
	for i, shot := range shots {
		x0, y0 := (i%cols)*cw, (i/cols)*ch
		for y := range ch {
			for x := range cw {
				out.Set(x0+x, y0+y, shot.At(x*3, y*3))
			}
		}
	}
	return out
}

// With MM_FRAMES set, saves the settings as they list the sources: the local
// files and a plugin that asks for a couple of things.
func TestSettingsFrame(t *testing.T) {
	dir := framesDir(t)
	a := newTestApp()
	a.libs = nil
	a.local = local.New(t.TempDir())
	addFake(a, a.local)
	plugin := newFakeSource("radio-garden")
	plugin.info.Name = "Example Source"
	plugin.info.Version = "1.2.0"
	plugin.info.Settings = []music.Setting{
		{Key: "apiKey", Label: "API Key", Help: "Keys are free for personal use.", Link: "https://example.com/keys"},
		{Key: "region", Label: "Region"},
	}
	plugin.vals["region"] = "Nordics"
	a.library = addFake(a, plugin)
	a.account = &music.Account{ID: "1", Name: "Alex"}
	a.settingsPage = "plugins"
	tt := ui.NewTester(a.settingsView, 780, 720)
	settled(tt)
	saveFrame(t, dir, "settings.png", tt.Image())
}

// With MM_FRAMES set, saves Now Playing with and without a radio: the
// radio chip used to lift the cover almost against the tabs.
func TestNowPlayingRadioFrames(t *testing.T) {
	dir := framesDir(t)
	for _, size := range [][2]int{{880, 580}, {900, 650}, {1200, 800}, {1400, 900}, {600, 1400}} {
		for _, radio := range []bool{false, true} {
			a := newTestApp()
			a.pl.Cue(framesTracks(a))
			a.pl.Hold(10 * time.Second)
			if radio {
				a.pl.AttachRadio(radioName("fake", &music.Station{ID: "station", Name: "Evening Radio"}), func(func([]music.Track)) {})
			}
			a.nowPlaying, a.npPane = true, paneQueue
			tt := ui.NewTester(a.view, size[0], size[1])
			tt.SetDark(true)
			settled(tt)
			tt.Move(float32(size[0])/2, 70)
			settled(tt)
			saveFrame(t, dir, fmt.Sprintf("np-radio-%t-%dx%d.png", radio, size[0], size[1]), tt.Image())
		}
	}
}

// With MM_FRAMES set, saves Now Playing in each retro player style, a third
// of the way through a song, playing and paused.
func TestRetroPlayerFrames(t *testing.T) {
	dir := framesDir(t)
	for _, style := range playerStyles[1:] {
		for _, playing := range []bool{true, false} {
			a := newTestApp()
			a.settings.PlayerStyle = style.id
			a.pl.Cue(framesTracks(a))
			a.pl.SetLyrics(nil)
			a.pl.Seek(60 * time.Second)
			name := "retro-" + style.id + "-paused.png"
			if playing {
				a.pl.Hold(60 * time.Second)
				name = "retro-" + style.id + ".png"
			}
			a.nowPlaying, a.npPane = true, paneQueue
			tt := ui.NewTester(a.view, 1180, 780)
			tt.SetDark(true)
			settled(tt)
			saveFrame(t, dir, name, tt.Image())
		}
	}
}
