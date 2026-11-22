package player

import (
	"testing"
	"time"

	"mygo-music/internal/music"
)

// 同曲目歌词被替换后，较早发起的加载不能覆盖新结果；清除后重新读取来源。
func TestLyricsOverrideReloadRejectsOlderLoads(t *testing.T) {
	posts := make(chan func(), 4)
	current := &Player{post: func(operation func()) { posts <- operation }}
	track := music.Track{Source: "test", ID: "song"}
	current.Cue([]music.Track{track})
	text := "first"
	current.LyricsOverride = func(music.Track) *music.Lyrics {
		if text == "" {
			return nil
		}
		return &music.Lyrics{LRC: "[00:01.000]" + text}
	}
	current.ReloadLyrics()
	older := <-posts
	text = "second"
	current.ReloadLyrics()
	newer := <-posts
	newer()
	older()
	source, loaded := current.LyricsSource()
	if !loaded || source == nil || source.LRC != "[00:01.000]second" {
		t.Fatal("old lyric load replaced a newer override")
	}
	text = ""
	current.ReloadLyrics()
	select {
	case operation := <-posts:
		operation()
	case <-time.After(time.Second):
		t.Fatal("clearing the override did not finish")
	}
	source, loaded = current.LyricsSource()
	if !loaded || source != nil {
		t.Fatal("cleared override remained in the player")
	}
}
