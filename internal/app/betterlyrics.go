package app

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"mygo-music/internal/betterlyrics"
	"mygo-music/internal/local"
	"mygo-music/internal/music"
)

func (a *app) setBetterLyricsIntegration() {
	if !a.settings.BetterLyricsIntegration {
		a.stopBetterLyricsIntegration()
		return
	}
	if a.betterLyricsServer != nil {
		a.betterLyricsServer.SetLyricsWriteEnabled(a.settings.BetterLyricsLyricsWrite)
		return
	}
	server := betterlyrics.NewServer(a.betterLyricsOverrides)
	server.SetLyricsWriteEnabled(a.settings.BetterLyricsLyricsWrite)
	// 校验与写入在主线程执行，避免 HTTP 快照尚未刷新时把旧请求写到已切换的歌曲。
	server.SetOverrideDispatcher(func(ctx context.Context, operation func()) error {
		done := make(chan struct{})
		a.post(func() {
			defer close(done)
			if ctx.Err() != nil {
				return
			}
			a.publishBetterLyrics()
			if a.betterLyricsServer == server {
				operation()
			}
		})
		select {
		case <-done:
			return ctx.Err()
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	server.SetLyricsOverrideChanged(func(key betterlyrics.SongKey) {
		if a.pl != nil && a.pl.Current() != nil && a.betterLyricsSongKey(*a.pl.Current()) == key {
			a.pl.ReloadLyrics()
		}
	})
	if err := server.Start(betterlyrics.DefaultAddress); err != nil {
		a.betterLyricsStatus = Tf("Could not start BetterLyrics integration: %s", err)
		log.Printf("starting BetterLyrics integration: %v", err)
		a.redraw()
		return
	}
	a.betterLyricsStatus = ""
	a.betterLyricsServer = server
	a.publishBetterLyrics()
	a.scheduleBetterLyricsSync()
	a.redraw()
}

func (a *app) betterLyricsSongKey(track music.Track) betterlyrics.SongKey {
	if track.Source == local.ID {
		if a.local == nil {
			return betterlyrics.SongKey{}
		}
		return betterlyrics.LocalSongKey(a.local.TrackPath(track.ID))
	}
	key := betterlyrics.KeyForTrack(track)
	if track.ID == "" && strings.HasPrefix(key.StableID, "metadata:") && a.pl != nil {
		matches := 0
		for _, candidate := range a.pl.Queue() {
			if betterlyrics.KeyForTrack(candidate) == key {
				matches++
			}
		}
		// 无来源 ID 时宁可拒绝重复候选，也不把同名不同版本的覆盖混用。
		if matches != 1 {
			return betterlyrics.SongKey{}
		}
	}
	return key
}

func (a *app) clearBetterLyricsOverride() {
	if a.pl == nil || a.pl.Current() == nil || a.betterLyricsOverrides == nil {
		return
	}
	if err := a.betterLyricsOverrides.Delete(a.betterLyricsSongKey(*a.pl.Current())); err != nil {
		a.toast = T("Could not restore source lyrics")
		return
	}
	if a.betterLyricsServer != nil {
		a.betterLyricsServer.InvalidateLyrics()
	}
	a.pl.ReloadLyrics()
	a.publishBetterLyrics()
}

func (a *app) stopBetterLyricsIntegration() {
	if a.betterLyricsTimer != nil {
		a.betterLyricsTimer.Stop()
		a.betterLyricsTimer = nil
	}
	if a.betterLyricsServer == nil {
		return
	}
	if err := a.betterLyricsServer.Close(); err != nil {
		log.Printf("stopping BetterLyrics integration: %v", err)
	}
	a.betterLyricsServer = nil
	a.redraw()
}

func (a *app) scheduleBetterLyricsSync() {
	server := a.betterLyricsServer
	if server == nil {
		return
	}
	delay := 250 * time.Millisecond
	if a.pl != nil && !a.pl.Playing() {
		// 排空设备缓冲后的最终位置可能晚于最后一次播放状态回调。
		delay = 500 * time.Millisecond
		if a.betterLyricsTimer != nil {
			a.betterLyricsTimer.Stop()
		}
	} else if a.betterLyricsTimer != nil {
		return
	}
	var scheduled *time.Timer
	scheduled = time.AfterFunc(delay, func() {
		mygo.RunOnMain(func() {
			if a.betterLyricsServer != server || a.betterLyricsTimer != scheduled {
				return
			}
			a.betterLyricsTimer = nil
			a.publishBetterLyrics()
			// 暂停先排空音频缓冲，补发最终实际位置后停止定时更新。
			if a.pl != nil && a.pl.Playing() {
				a.scheduleBetterLyricsSync()
			}
		})
	})
	a.betterLyricsTimer = scheduled
}

func (a *app) publishBetterLyrics() {
	if a.betterLyricsServer == nil || a.pl == nil {
		return
	}
	status := betterlyrics.Status{}
	var lyrics *betterlyrics.Lyrics
	if track := a.pl.Current(); track != nil {
		key := track.Source + "\x00" + track.ID + "\x00" + track.PlaybackID + "\x00" + track.Name + "\x00" + track.ArtistNames() + "\x00" + track.Album.Name
		if key != a.betterLyricsTrackKey {
			a.betterLyricsTrackID++
			a.betterLyricsTrackKey = key
		}
		status = betterlyrics.Status{
			TrackID:    a.betterLyricsTrackID,
			SongKey:    a.betterLyricsSongKey(*track),
			Title:      track.Name,
			Artist:     track.ArtistNames(),
			Album:      track.Album.Name,
			Playing:    a.pl.Playing(),
			PositionMs: a.pl.Position().Milliseconds(),
			DurationMs: a.pl.Duration().Milliseconds(),
		}
		if source, loaded := a.pl.LyricsSource(); loaded && source != nil {
			origin := ""
			if a.betterLyricsOverrides != nil && a.betterLyricsOverrides.Has(status.SongKey) {
				origin = "override"
			}
			lyrics = &betterlyrics.Lyrics{
				TrackID:     status.TrackID,
				SongKey:     status.SongKey,
				Origin:      origin,
				LRC:         source.LRC,
				Translation: source.Translation,
				Lines:       source.Lines,
			}
		}
	} else {
		a.betterLyricsTrackKey = ""
	}
	a.betterLyricsServer.Publish(status, lyrics)
}
