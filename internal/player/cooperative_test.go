package player_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"mygo-music/internal/music"
	"mygo-music/internal/player"
	"mygo-music/internal/plugin"
)

// 聚合分享也必须走已核验的协作取流，不能调用未实现的旧 stream 或换成其他录音。
func TestFindSongPreservesCooperativeRecording(t *testing.T) {
	source := cooperativePlugin(t, fmt.Sprintf(`
export function stream() { throw new Error("legacy stream must not be used"); }
export function playbackPlan({id}) {
 if(id !== "played-recording") throw new Error("lost played recording");
 return {minimumQuality:"128k", candidates:[{id:"one",platform:"wy",accountID:"a",trackID:"raw",quality:"flac",playbackID:"played-recording"}]};
}
export function resolveCandidate() { return {stream:{url:%s}}; }
`, audioPath(t, "seek.flac")))
	current, _ := cooperativePlayer(source)
	song, err := current.FindSong(source, music.Track{ID: "queued-recording", Source: "cooperative", PlaybackID: "played-recording"}, "lossless")
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "shared.flac")
	if err := song.SaveTo(output); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/seek.flac")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if song.Type != "flac" || !bytes.Equal(got, want) {
		t.Fatal("shared media differs from verified recording")
	}
}

func cooperativePlugin(t *testing.T, source string) *plugin.Plugin {
	t.Helper()
	dir := t.TempDir()
	for name, contents := range map[string]string{
		"plugin.json": `{"id":"cooperative","permissions":{"network":["127.0.0.1"]}}`,
		"index.js":    source,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := plugin.Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func cooperativePlayer(src music.Source) (*player.Player, chan func()) {
	posts := make(chan func(), 64)
	p := player.New(func(fn func()) { posts <- fn })
	p.Source = func(string) music.Source { return src }
	return p, posts
}

func finishLoading(t *testing.T, p *player.Player, posts <-chan func()) {
	t.Helper()
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	for p.Loading() {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatal("player did not complete loading")
		}
	}
}

func audioPath(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal("file:///" + filepath.ToSlash(path))
	return string(b)
}

// 同档第一来源失败后继续下一来源，实际 FLAC 不依赖服务返回的音质标签。
func TestCooperativePluginFallsThroughToVerifiedAudio(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
export function stream() { throw new Error("legacy stream must not be used"); }
export function playbackPlan() { return {
  targetQuality: "flac", minimumQuality: "128k", candidates: [
    {id: "a", platform: "wy", trackID: "raw-a", quality: "flac"},
    {id: "b", platform: "tx", trackID: "raw-b", quality: "flac"}
  ]
}; }
export function resolveCandidate({candidate, roundID}) {
  if (!roundID) throw new Error("missing round identity");
  if (candidate.id === "a") throw Object.assign(new Error("unavailable"), {code: "unavailable"});
  return {stream: {url: %s, type: "mp3", quality: "32k"}, platform: "lying", requestedQuality: "flac"};
}`, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "aggregate:1", Source: "cooperative", Duration: 1000}}, 0)
	finishLoading(t, p, posts)
	if !p.Ready() || p.Err() != "" {
		t.Fatalf("cooperative playback not ready: ready=%v err=%q", p.Ready(), p.Err())
	}
	if got := p.Stream(); got == nil || got.Quality != "flac" || got.Type != "flac" {
		t.Fatalf("actual media evidence not used: %+v", got)
	}
	p.TogglePlay()
}

// 同平台同账号同曲目同档的备用 provider 曾被去重跳过；解析或媒体准备失败后仍须实际开播备用。
func TestCooperativeSameTrackProvidersFallThroughToVerifiedAudio(t *testing.T) {
	for _, tc := range []struct{ name, first string }{
		{"resolver", `throw Object.assign(new Error("provider unavailable"), {code:"unavailable"});`},
		{"media", `return {stream:{url:"file:///missing-custom-source-fixture.flac"}};`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidates:[
{id:"a",platform:"wy",accountID:"same",trackID:"one",quality:"flac",sourceID:"primary",sourceRevision:"r1"},
{id:"b",platform:"wy",accountID:"same",trackID:"one",quality:"flac",sourceID:"backup",sourceRevision:"r2"}]}; }
export function resolveCandidate({candidate}) {
 if(candidate.id === "a") { %s }
 if(candidate.sourceID !== "backup" || candidate.sourceRevision !== "r2") throw new Error("provider identity lost");
 return {stream:{url:%s,type:"mp3",quality:"32k"}};
}
`, tc.first, audioPath(t, "seek.flac")))
			p, posts := cooperativePlayer(src)
			p.SetVolume(0)
			p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
			finishLoading(t, p, posts)
			defer func() {
				if p.Playing() || p.Loading() {
					p.TogglePlay()
				}
			}()
			if !p.Ready() || !p.Playing() || p.Err() != "" || p.Stream() == nil || p.Stream().Quality != "flac" || p.Stream().Type != "flac" {
				t.Fatalf("同曲备用 provider 未实际开播已核验媒体：ready=%v playing=%v stream=%+v error=%q", p.Ready(), p.Playing(), p.Stream(), p.Err())
			}
		})
	}
}

// 来源在媒体准备后被停用、删除或替换时，旧结果不能迟到开播；同平台备用仍在原轮次继续。
func TestCooperativeSourceControlsInvalidatePreparedProvider(t *testing.T) {
	for _, action := range []string{"setSourceEnabled", "removeSource", "replaceSource"} {
		t.Run(action, func(t *testing.T) {
			src := cooperativePlugin(t, fmt.Sprintf(`
let invalidated = false;
export function aggregate({input}) { if(input.id === "primary") invalidated = true; return {}; }
export function candidateValid({candidate}) {
 if(candidate.sourceRevision !== "r1") throw new Error("source revision lost");
 return candidate.sourceID !== "primary" || !invalidated;
}
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidates:[
{id:"a",platform:"wy",accountID:"a",trackID:"one",quality:"flac",sourceID:"primary",sourceRevision:"r1",playbackID:"played-primary"},
{id:"b",platform:"wy",accountID:"a",trackID:"one",quality:"flac",sourceID:"backup",sourceRevision:"r1",playbackID:"played-backup"}]}; }
export function resolveCandidate() { return {stream:{url:%s}}; }
`, audioPath(t, "seek.flac")))
			p, posts := cooperativePlayer(src)
			p.SetVolume(0)
			invalidated := false
			p.Normalize = func() bool {
				if !invalidated {
					invalidated = true
					if _, err := src.Aggregate(action, map[string]any{"id": "primary", "enabled": false, "url": "synthetic-replacement"}); err != nil {
						t.Fatal(err)
					}
				}
				return false
			}
			p.PlayTrack([]music.Track{{ID: "queued", Source: "cooperative"}, {ID: "next", Source: "cooperative"}}, 0)
			finishLoading(t, p, posts)
			defer func() {
				if p.Playing() || p.Loading() {
					p.TogglePlay()
				}
			}()
			if !invalidated || !p.Ready() || !p.Playing() || p.Stream() == nil || p.Stream().PlaybackID != "played-backup" || p.Current().ID != "queued" || len(p.Queue()) != 2 || p.Err() != "" {
				t.Fatalf("收紧后的旧 provider 仍开播或未继续备用：stream=%+v current=%+v queue=%+v error=%q", p.Stream(), p.Current(), p.Queue(), p.Err())
			}
		})
	}
}

// 同一聚合身份重复入队时，拆分后仅已播位置跟随实际来源，未播位置仍用入队锚点。
func TestCooperativeSplitKeepsPlayedVersionWithoutChangingQueue(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
let split = false;
export function aggregate() { split = true; return {ok:true}; }
export function playbackPlan({id}) {
 const played = id === "played-wy";
 if (id !== "queued-anchor" && !played) throw new Error("unexpected playback identity:" + id);
 const platform = !split || played ? "wy" : "tx";
 return {targetQuality:"flac",minimumQuality:"128k",candidates:[
 {id:"candidate",platform,accountID:"a",trackID:platform,quality:"flac",playbackID:platform==="wy"?"played-wy":"played-tx"}]};
}
export function resolveCandidate() { return {stream:{url:%s,playbackID:"untrusted-response"}}; }
`, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	queue := []music.Track{{ID: "queued-anchor", Source: "cooperative", Name: "first"}, {ID: "queued-anchor", Source: "cooperative", Name: "second"}}
	p.PlayTrack(queue, 0)
	finishLoading(t, p, posts)
	if !p.Ready() || p.Stream().Platform != "wy" {
		t.Fatalf("实际来源没有成功开播：%+v %q", p.Stream(), p.Err())
	}
	session, ok := p.Session()
	if !ok || session.Queue[0].PlaybackID != "played-wy" || session.Queue[1].PlaybackID != "" {
		t.Fatalf("成功播放的版本没有按队列位置隔离：%+v", session)
	}
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	var restored player.Session
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	p.TogglePlay()
	if _, err := src.Aggregate("revokeLink", nil); err != nil {
		t.Fatal(err)
	}
	p, posts = cooperativePlayer(src)
	p.Restore(restored)
	finishLoading(t, p, posts)
	if !p.Ready() || p.Playing() || p.Stream().Platform != "wy" {
		t.Fatalf("会话恢复丢失已播版本或自动开播：stream=%+v error=%q", p.Stream(), p.Err())
	}
	p.TogglePlay()
	finishLoading(t, p, posts)
	if !p.Ready() || !p.Playing() || p.Stream().Platform != "wy" || p.Current().PlaybackID != "played-wy" {
		t.Fatalf("恢复后继续播放改换了已播版本：stream=%+v current=%+v error=%q", p.Stream(), p.Current(), p.Err())
	}
	p.Jump(0)
	finishLoading(t, p, posts)
	if !p.Ready() || p.Stream().Platform != "wy" {
		t.Fatalf("拆分后已播项改换版本：stream=%+v error=%q", p.Stream(), p.Err())
	}
	p.Jump(1)
	finishLoading(t, p, posts)
	defer p.TogglePlay()
	if !p.Ready() || p.Stream().Platform != "tx" {
		t.Fatalf("未播位置被同身份已播项污染：stream=%+v error=%q", p.Stream(), p.Err())
	}
	got := p.Queue()
	if len(got) != 2 || got[0].ID != queue[0].ID || got[1].ID != queue[1].ID || got[0].Name != "first" || got[1].Name != "second" || p.Current().ID != "queued-anchor" {
		t.Fatalf("拆分改变队列身份、数量或顺序：%+v", got)
	}
}

// 恢复时仅准备的流还不是已播版本，用户首次开播后才应固定该队列位置的版本。
func TestCooperativeResumedSessionRecordsFirstPlayedVersion(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
let split = false;
export function aggregate() { split = true; return {ok:true}; }
export function playbackPlan({id}) {
 const platform = !split || id === "played-wy" ? "wy" : "tx";
 return {targetQuality:"flac",minimumQuality:"128k",candidates:[
 {id:"candidate",platform,trackID:platform,quality:"flac",playbackID:platform==="wy"?"played-wy":"played-tx"}]};
}
export function resolveCandidate() { return {stream:{url:%s}}; }
`, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.Restore(player.Session{
		Queue: []music.Track{{ID: "queued-anchor", Source: "cooperative"}, {ID: "queued-anchor", Source: "cooperative"}},
		Order: []int{0, 1},
	})
	finishLoading(t, p, posts)
	if !p.Ready() || p.Playing() || p.Queue()[0].PlaybackID != "" {
		t.Fatalf("暂停预加载被当作成功开播：ready=%v playing=%v queue=%+v error=%q", p.Ready(), p.Playing(), p.Queue(), p.Err())
	}
	p.TogglePlay()
	finishLoading(t, p, posts)
	defer p.TogglePlay()
	if !p.Playing() || p.Current().PlaybackID != "played-wy" || p.Queue()[0].PlaybackID != "played-wy" || p.Queue()[1].PlaybackID != "" {
		t.Fatalf("恢复后首次开播没有仅记录当前位置：current=%+v queue=%+v", p.Current(), p.Queue())
	}
	if _, err := src.Aggregate("revokeLink", nil); err != nil {
		t.Fatal(err)
	}
	p.Jump(0)
	finishLoading(t, p, posts)
	if !p.Ready() || p.Stream().Platform != "wy" {
		t.Fatalf("恢复后开播的版本在拆分后丢失：stream=%+v error=%q", p.Stream(), p.Err())
	}
}

// 恢复后尚未播放的预加载不能固定版本，拆分后首次播放必须重新采用原入队锚点。
func TestCooperativeUnplayedSessionRechecksSplitBeforeFirstPlay(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
let split = false;
export function aggregate() { split = true; return {ok:true}; }
export function playbackPlan({id}) {
 if (id !== "queued-anchor") throw new Error("unplayed item changed identity");
 const platform = split ? "tx" : "wy";
 return {targetQuality:"flac",minimumQuality:"128k",candidates:[
 {id:"candidate",platform,trackID:platform,quality:"flac",playbackID:"played-"+platform}]};
}
export function resolveCandidate() { return {stream:{url:%s}}; }
`, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.Restore(player.Session{Queue: []music.Track{{ID: "queued-anchor", Source: "cooperative"}}, Order: []int{0}})
	finishLoading(t, p, posts)
	if !p.Ready() || p.Playing() || p.Stream().Platform != "wy" || p.Current().PlaybackID != "" {
		t.Fatalf("暂停预加载状态错误：stream=%+v current=%+v error=%q", p.Stream(), p.Current(), p.Err())
	}
	if _, err := src.Aggregate("revokeLink", nil); err != nil {
		t.Fatal(err)
	}
	p.TogglePlay()
	finishLoading(t, p, posts)
	defer p.TogglePlay()
	if !p.Ready() || !p.Playing() || p.Stream().Platform != "tx" || p.Current().ID != "queued-anchor" || p.Queue()[0].PlaybackID != "played-tx" {
		t.Fatalf("首次播放沿用了拆分前的未播版本：stream=%+v current=%+v error=%q", p.Stream(), p.Current(), p.Err())
	}
}

// 暂停正在解析的聚合项必须中止真实请求，且不跳歌、不记作失败。
func TestCooperativePauseCancelsResolverRequest(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac", minimumQuality:"128k", candidates:[{id:"a",platform:"wy",trackID:"a",quality:"flac"}]}; }
export async function resolveCandidate({signal}) {
 const response = await fetch(%q, {signal});
 return response.json();
}
`, server.URL))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("resolver request did not start")
	}
	p.TogglePlay()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		server.CloseClientConnections()
		t.Fatal("pause left resolver network request running")
	}
	for len(posts) > 0 {
		(<-posts)()
	}
	if p.Playing() || p.Loading() || p.Err() != "" || p.Current().ID != "one" || len(p.Queue()) != 2 {
		t.Fatalf("pause changed playback intent: playing=%v loading=%v error=%q current=%+v", p.Playing(), p.Loading(), p.Err(), p.Current())
	}
}

// 撤销使正在解析的来源失效时，必须取消真实请求，在原轮次继续有效来源且不改队列。
func TestCooperativeRevokingLinkCancelsInFlightCandidate(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	src := cooperativePlugin(t, fmt.Sprintf(`
let revoked = false;
export function aggregate({action}) { if(action === "revokeLink") revoked = true; return {ok:true}; }
export function candidateValid({candidate}) { return candidate.platform !== "wy" || !revoked; }
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidates:[
{id:"first",platform:"wy",accountID:"a",trackID:"one",quality:"flac"},
{id:"anchor",platform:"tx",accountID:"b",trackID:"two",quality:"flac"}]}; }
export async function resolveCandidate({candidate,signal}) {
 if(candidate.id === "first") return (await fetch(%q,{signal})).json();
 return {stream:{url:%s}};
}
`, server.URL, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "queued", Source: "cooperative"}, {ID: "next", Source: "cooperative"}}, 0)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		p.TogglePlay()
		t.Fatal("关联候选的解析没有开始")
	}
	if _, err := src.Aggregate("revokeLink", nil); err != nil {
		p.TogglePlay()
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		p.TogglePlay()
		server.CloseClientConnections()
		t.Fatal("撤销关联后仍在等待失效来源的解析请求")
	}
	finishLoading(t, p, posts)
	if !p.Ready() || p.Stream() == nil || p.Stream().Platform != "tx" || p.Err() != "" || p.Current().ID != "queued" || len(p.Queue()) != 2 {
		t.Fatalf("撤销没有在原队列继续有效来源：ready=%v stream=%+v error=%q current=%+v queue=%+v", p.Ready(), p.Stream(), p.Err(), p.Current(), p.Queue())
	}
	p.TogglePlay()
}

// 撤销后整组候选失效是用户改变关联，不算歌曲失败，也不能自动扫描下一项。
func TestCooperativeRevocationExhaustionStaysOnCurrentTrack(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	src := cooperativePlugin(t, fmt.Sprintf(`
let revoked = false;
export function aggregate() { revoked = true; return {ok:true}; }
export function candidateValid() { return !revoked; }
export function playbackPlan({id}) { return {targetQuality:"flac",minimumQuality:"128k",candidates:[{id,platform:"wy",trackID:id,quality:"flac"}]}; }
export async function resolveCandidate({signal}) { return (await fetch(%q,{signal})).json(); }
`, server.URL))
	p, posts := cooperativePlayer(src)
	p.PlayQueue([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		p.TogglePlay()
		t.Fatal("解析没有开始")
	}
	if _, err := src.Aggregate("revokeLink", nil); err != nil {
		p.TogglePlay()
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		p.TogglePlay()
		server.CloseClientConnections()
		t.Fatal("撤销没有中止失效请求")
	}
	finishLoading(t, p, posts)
	if p.Ready() || p.Playing() || p.Err() != "" || p.Current().ID != "one" || len(p.Queue()) != 2 {
		t.Fatalf("撤销被计为最终失败或改变队列：ready=%v playing=%v error=%q current=%+v queue=%+v", p.Ready(), p.Playing(), p.Err(), p.Current(), p.Queue())
	}
}

// 撤销已经返回但观察器仍等待查询时，已进入主线程准备的旧结果也不能开播。
func TestCooperativeRevocationDuringPreparationCannotPlayLate(t *testing.T) {
	checking, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(checking)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		fmt.Fprint(w, "false")
	}))
	defer server.Close()
	defer close(release)
	src := cooperativePlugin(t, fmt.Sprintf(`
let revoked = false, checks = 0;
export function aggregate() { revoked = true; return {ok:true}; }
export async function candidateValid({signal}) {
 if (!revoked) return true;
 if (++checks === 1) return (await fetch(%q,{signal})).json();
 return false;
}
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidates:[{id:"a",platform:"wy",trackID:"a",quality:"flac",playbackID:"not-started"}]}; }
export function resolveCandidate() { return {stream:{url:%s}}; }
`, server.URL, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.Normalize = func() bool {
		if _, err := src.Aggregate("revokeLink", nil); err != nil {
			t.Fatal(err)
		}
		select {
		case <-checking:
		case <-time.After(2 * time.Second):
			t.Fatal("撤销观察器没有开始校验")
		}
		return false
	}
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	defer p.TogglePlay()
	if p.Ready() || p.Playing() || p.Stream() != nil || p.Err() != "" || p.Current().ID != "one" || len(p.Queue()) != 2 {
		t.Fatalf("已撤销的准备结果仍被开播：ready=%v playing=%v stream=%+v error=%q current=%+v", p.Ready(), p.Playing(), p.Stream(), p.Err(), p.Current())
	}
	if p.Current().PlaybackID != "" || p.Queue()[0].PlaybackID != "" || p.Queue()[1].PlaybackID != "" {
		t.Fatalf("未开播的撤销候选污染版本记录：current=%+v queue=%+v", p.Current(), p.Queue())
	}
}

// 主线程已交接而后台仍卡在 post 返回时，撤销和原候选截止都不能关闭播放下载。
func TestCooperativeRevocationAfterHandoffKeepsPlayingDownload(t *testing.T) {
	audio, err := os.ReadFile(filepath.Join("testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	deliver, served, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
		w.Write(audio[:len(audio)/2])
		w.(http.Flusher).Flush()
		select {
		case <-deliver:
			w.Write(audio[len(audio)/2:])
			close(served)
		case <-r.Context().Done():
			close(canceled)
		}
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	src := cooperativePlugin(t, fmt.Sprintf(`
let revoked = false;
export function aggregate() { revoked = true; return {ok:true}; }
export function candidateValid() { return !revoked; }
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidateTimeoutMs:200,roundTimeoutMs:2000,candidates:[{id:"a",platform:"wy",trackID:"a",quality:"flac"}]}; }
export function resolveCandidate() { return {stream:{url:%q}}; }
`, server.URL))
	posts, releasePost := make(chan func(), 64), make(chan struct{})
	defer close(releasePost)
	p := player.New(func(fn func()) {
		posts <- fn
		// 公开 post 边界可停住后台接收回执，不阻塞主线程执行提交。
		<-releasePost
	})
	p.Source = func(string) music.Source { return src }
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if !p.Ready() || !p.Playing() {
		t.Fatalf("未完成音频交接：%q", p.Err())
	}
	defer p.TogglePlay()
	if _, err := src.Aggregate("revokeLink", nil); err != nil {
		t.Fatal(err)
	}
	// 等待既定候选截止而非等待观察器调度：后台回执仍未放行，已开播的流必须脱离候选生命周期。
	deadline := time.NewTimer(250 * time.Millisecond)
	defer deadline.Stop()
	select {
	case <-canceled:
		t.Fatal("撤销错误关闭了已开播下载")
	case <-deadline.C:
	}
	close(deliver)
	select {
	case <-canceled:
		t.Fatal("候选截止错误关闭了已开播下载")
	case <-served:
	case <-time.After(2 * time.Second):
		t.Fatal("已开播下载未继续读取")
	}
	timer, tick := time.NewTimer(2*time.Second), time.NewTicker(time.Millisecond)
	defer timer.Stop()
	defer tick.Stop()
	for {
		if fraction, ok := p.Downloaded(); ok && fraction == 1 {
			break
		}
		select {
		case <-canceled:
			t.Fatal("撤销后播放下载被取消")
		case <-timer.C:
			t.Fatal("撤销后没有下载完整音频")
		case <-tick.C:
		}
	}
	if !p.Ready() || !p.Playing() || p.Current().ID != "one" || p.Err() != "" {
		t.Fatalf("交接后撤销改变了当前播放：ready=%v playing=%v error=%q", p.Ready(), p.Playing(), p.Err())
	}
}

// 首源返回达下限的低档音频也不能抢过同档下一源的真实 FLAC。
func TestCooperativeLowerActualQualityDoesNotPreemptFLAC(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidates:[
{id:"a",platform:"wy",trackID:"a",quality:"flac"},{id:"b",platform:"tx",trackID:"b",quality:"flac"},
{id:"c",platform:"wy",trackID:"a",quality:"128k"}]}; }
export function resolveCandidate({candidate}) { return {stream:{url:candidate.id==="b"?%s:%s,type:"flac",quality:"flac"}}; }
`, audioPath(t, "seek.flac"), audioPath(t, "seek.mp3")))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if !p.Ready() || p.Stream().Quality != "flac" || p.Stream().Platform != "tx" {
		t.Fatalf("lower-quality response preempted FLAC: ready=%v stream=%+v error=%q", p.Ready(), p.Stream(), p.Err())
	}
	p.TogglePlay()
}

// 高级名称本身不是媒体证据；返回普通低档时必须先走普通 FLAC 路径。
func TestCooperativeAdvancedTargetFallsBackToOrdinaryFLAC(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"immersive",minimumQuality:"128k",candidates:[
{id:"advanced",platform:"wy",trackID:"a",quality:"immersive"},
{id:"ordinary",platform:"tx",trackID:"b",quality:"flac"}]}; }
export function resolveCandidate({candidate}) { return {stream:{url:candidate.id==="advanced"?%s:%s}}; }
`, audioPath(t, "seek.mp3"), audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if !p.Ready() || p.Stream().Quality != "flac" || p.Stream().Platform != "tx" {
		t.Fatalf("unverified advanced quality bypassed ordinary FLAC: ready=%v stream=%+v error=%q", p.Ready(), p.Stream(), p.Err())
	}
	p.TogglePlay()
}

// 较低但合格的结果只在同来源对应档位轮到时复用，不能重复解析或抢先开播。
func TestCooperativeReusesVerifiedLowerQualityOnlyAtItsTurn(t *testing.T) {
	calls := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls <- r.URL.Path
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidates:[
{id:"wy-flac",platform:"wy",accountID:"a",trackID:"one",quality:"flac"},
{id:"tx-flac",platform:"tx",accountID:"b",trackID:"two",quality:"flac"},
{id:"wy-128k",platform:"wy",accountID:"a",trackID:"one",quality:"128k"}]}; }
export async function resolveCandidate({candidate,signal}) {
 await fetch(%q + "/" + candidate.id, {signal});
 if(candidate.id !== "wy-flac") throw new Error("resolver called:"+candidate.id);
 return {stream:{url:%s,type:"flac",quality:"flac"}};
}
`, server.URL, audioPath(t, "seek.mp3")))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if !p.Ready() || p.Stream().Quality != "128k" || p.Stream().Platform != "wy" {
		t.Fatalf("同档耗尽后没有复用已核验结果: ready=%v stream=%+v error=%q", p.Ready(), p.Stream(), p.Err())
	}
	defer p.TogglePlay()
	var got []string
	for len(calls) > 0 {
		got = append(got, <-calls)
	}
	if len(got) != 2 || got[0] != "/wy-flac" || got[1] != "/tx-flac" {
		t.Fatalf("后备越过未完成的 FLAC 来源，或重复解析了同轮结果: %v", got)
	}
}

// 同曲低档后备曾跨 provider 复用；轮到备用 provider 时必须解析它自己的地址，不能冒用首源媒体。
func TestCooperativeLowerQualityFallbackIsolatedByProvider(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidates:[
{id:"primary-flac",platform:"wy",accountID:"a",trackID:"one",quality:"flac",sourceID:"primary"},
{id:"backup-128k",platform:"wy",accountID:"a",trackID:"one",quality:"128k",sourceID:"backup"}]}; }
export function resolveCandidate({candidate}) {
 if(candidate.sourceID === "backup") return {stream:{url:%s,type:"mp3",quality:"32k"}};
 return {stream:{url:%s,type:"flac",quality:"flac"}};
}
`, audioPath(t, "seek.flac"), audioPath(t, "seek.mp3")))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	defer func() {
		if p.Playing() || p.Loading() {
			p.TogglePlay()
		}
	}()
	if !p.Ready() || !p.Playing() || p.Stream() == nil || p.Stream().Quality != "flac" || p.Stream().Type != "flac" || p.Err() != "" {
		t.Fatalf("备用 provider 复用了其他来源的低档地址：ready=%v stream=%+v error=%q", p.Ready(), p.Stream(), p.Err())
	}
}

// 后备地址必须重新打开；失效后继续下一来源，不能播放此前已丢弃的媒体。
func TestCooperativeExpiredFallbackContinuesNextSource(t *testing.T) {
	var opened atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if opened.Add(1) != 1 {
			http.Error(w, "expired", http.StatusForbidden)
			return
		}
		http.ServeFile(w, r, filepath.Join("testdata", "seek.mp3"))
	}))
	defer server.Close()
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan(){return {targetQuality:"flac",minimumQuality:"128k",candidates:[
{id:"wy-flac",platform:"wy",accountID:"a",trackID:"one",quality:"flac"},
{id:"wy-128k",platform:"wy",accountID:"a",trackID:"one",quality:"128k"},
{id:"tx-128k",platform:"tx",accountID:"b",trackID:"two",quality:"128k"}]};}
export function resolveCandidate({candidate}) {
 if(candidate.id==="wy-128k") throw new Error("fallback resolved twice");
 return {stream:{url:candidate.id==="wy-flac"?%q:%s}};
}
`, server.URL, audioPath(t, "seek.mp3")))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if !p.Ready() || p.Stream().Platform != "tx" || p.Stream().Quality != "128k" || opened.Load() != 2 {
		t.Fatalf("失效后备没有重新检查或阻止下一来源: ready=%v stream=%+v opens=%d error=%q", p.Ready(), p.Stream(), opened.Load(), p.Err())
	}
	p.TogglePlay()
}

// 新播放轮次不得沿用上一轮的解析地址，即使歌曲与候选身份完全相同。
func TestCooperativeFallbackDoesNotSurviveIntoAnotherRound(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
let rounds=0;
export function playbackPlan(){rounds++;return {targetQuality:"flac",minimumQuality:"128k",candidates:[
{id:"flac",platform:"wy",accountID:"a",trackID:"one",quality:"flac"},
{id:"128k",platform:"wy",accountID:"a",trackID:"one",quality:"128k"}]};}
export function resolveCandidate({candidate}) {
 if(rounds>1) throw new Error("new round needs fresh resolution");
 if(candidate.id==="128k") throw new Error("fallback resolved twice");
 return {stream:{url:%s}};
}
`, audioPath(t, "seek.mp3")))
	p, posts := cooperativePlayer(src)
	tracks := []music.Track{{ID: "one", Source: "cooperative"}}
	p.PlayTrack(tracks, 0)
	finishLoading(t, p, posts)
	if !p.Ready() || p.Stream().Quality != "128k" {
		t.Fatalf("首轮未建立合格后备: stream=%+v error=%q", p.Stream(), p.Err())
	}
	p.PlayTrack(tracks, 0)
	finishLoading(t, p, posts)
	if p.Ready() || p.Playing() || p.Err() != "new round needs fresh resolution" {
		t.Fatalf("新轮复用了旧地址: ready=%v playing=%v error=%q", p.Ready(), p.Playing(), p.Err())
	}
}

// 即使已有合格后备，整轮耗尽后也不能重新打开它；保留当前项并结束首次播放。
func TestCooperativeRoundTimeoutDoesNotOpenCachedFallback(t *testing.T) {
	var opened atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opened.Add(1)
		http.ServeFile(w, r, filepath.Join("testdata", "seek.mp3"))
	}))
	defer server.Close()
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan(){return {targetQuality:"flac",minimumQuality:"128k",candidateTimeoutMs:1000,roundTimeoutMs:500,candidates:[
{id:"wy-flac",platform:"wy",accountID:"a",trackID:"one",quality:"flac"},
{id:"tx-flac",platform:"tx",accountID:"b",trackID:"two",quality:"flac"},
{id:"wy-128k",platform:"wy",accountID:"a",trackID:"one",quality:"128k"}]};}
export function resolveCandidate({candidate,signal}) {
 if(candidate.id==="wy-flac") return {stream:{url:%q}};
 if(candidate.id==="tx-flac") return new Promise((resolve,reject)=>signal.addEventListener("abort",()=>reject(new Error("aborted")),{once:true}));
 throw new Error("expired fallback was resolved");
}
`, server.URL))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if p.Ready() || p.Playing() || p.Stream() != nil || p.Err() == "" || p.Current().ID != "one" || opened.Load() != 1 {
		t.Fatalf("超时后复用了后备或改变当前项：ready=%v playing=%v stream=%+v opens=%d current=%+v error=%q", p.Ready(), p.Playing(), p.Stream(), opened.Load(), p.Current(), p.Err())
	}
}

// 聚合候选必须由当前播放轮次掌控，不能被旧预取接口提前访问。
func TestCooperativeDoesNotUseLegacyPrefetch(t *testing.T) {
	legacy := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case legacy <- struct{}{}:
		default:
		}
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidates:[{id:"a",platform:"wy",trackID:"a",quality:"flac"}]}; }
export function resolveCandidate() { return {stream:{url:%s,type:"flac"}}; }
export async function stream() { await fetch(%q); throw new Error("legacy prefetch was used"); }
`, audioPath(t, "seek.flac"), server.URL))
	p, posts := cooperativePlayer(src)
	p.PlayQueue([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	defer p.TogglePlay()
	select {
	case <-legacy:
		t.Fatal("cooperative source bypassed through legacy prefetch")
	case <-time.After(100 * time.Millisecond):
	}
}

// 实际有损流即使声称 FLAC，也不能突破用户的无损下限。
func TestCooperativeRejectsAudioBelowMinimum(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"flac",candidates:[{id:"a",platform:"wy",trackID:"a",quality:"flac"}]}; }
export function resolveCandidate() { return {stream:{url:%s,type:"flac",quality:"flac",bitRate:999999}}; }
`, audioPath(t, "seek.mp3")))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if p.Ready() || p.Playing() || p.Err() == "" || p.Current().ID != "one" {
		t.Fatalf("below-minimum audio accepted: ready=%v playing=%v err=%q", p.Ready(), p.Playing(), p.Err())
	}
}

// 单曲循环的最终失败不是正常结束，不能顺着队列换到下一首。
func TestCooperativeRepeatOneFailureStaysOnCurrentTrack(t *testing.T) {
	src := cooperativePlugin(t, `
export function playbackPlan({id}) { return {targetQuality:"flac",minimumQuality:"128k",candidates:[{id,platform:"wy",trackID:id,quality:"flac"}]}; }
export function resolveCandidate({id}) { throw new Error("failed:"+id); }
`)
	p, posts := cooperativePlayer(src)
	p.CycleRepeat()
	p.CycleRepeat()
	p.PlayQueue([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if p.Playing() || p.Current().ID != "one" || p.Err() != "failed:one" {
		t.Fatalf("repeat-one failure advanced queue: current=%+v playing=%v error=%q", p.Current(), p.Playing(), p.Err())
	}
}

// 每项候选耗尽才计一次最终失败；默认连续三项后不得再扫描第四项。
func TestCooperativeQueueStopsAfterThreeFinalFailures(t *testing.T) {
	src := cooperativePlugin(t, `
export function playbackPlan({id}) { return {targetQuality:"flac",minimumQuality:"128k",candidates:[{id,platform:"wy",trackID:id,quality:"flac"}]}; }
export function resolveCandidate({id}) { throw new Error("failed:"+id); }
`)
	p, posts := cooperativePlayer(src)
	p.PlayQueue([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}, {ID: "three", Source: "cooperative"}, {ID: "four", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if p.Playing() || p.Current().ID != "three" || p.Err() != "failed:three" {
		t.Fatalf("default failure limit ignored: current=%+v playing=%v error=%q", p.Current(), p.Playing(), p.Err())
	}
}

// 加载中开启随机播放后，失败应从当前项的新位置继续，不能重复当前项而漏掉另一首。
func TestCooperativeShuffleDuringLoadingSkipsFromCurrentPosition(t *testing.T) {
	src := cooperativePlugin(t, `
let visited=[];
export function aggregate() { return {visited}; }
export function playbackPlan({id}) {
 visited.push(id);
 return {targetQuality:"flac",minimumQuality:"128k",candidates:[
 {id,platform:"wy",trackID:id,quality:"flac"}]};
}
export function resolveCandidate({id}) { throw new Error("failed:"+id); }
`)
	p, posts := cooperativePlayer(src)
	p.PlayQueue([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 1)
	p.ToggleShuffle()
	finishLoading(t, p, posts)
	raw, err := src.Aggregate("state", nil)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Visited []string `json:"visited"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(state.Visited) != "[two one]" || p.Playing() || p.Current().ID != "one" || p.Err() != "failed:one" || len(p.Queue()) != 2 {
		t.Fatalf("加载中重排导致重复或漏试：visited=%v current=%+v error=%q", state.Visited, p.Current(), p.Err())
	}
}

// 关闭随机播放改变后续顺序时，同一失败区间仍应跳过已失败位置，继续尚未尝试的一首。
func TestCooperativeShuffleKeepsFailedQueuePositions(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
let visited=[];
export function aggregate() { return {visited}; }
export function playbackPlan({id}) {
 visited.push(id);
 return {targetQuality:"flac",minimumQuality:"128k",candidates:[
 {id,platform:"wy",trackID:id,quality:"flac"}]};
}
export function resolveCandidate({id}) {
 if(id!=="one") throw new Error("failed:"+id);
 return {stream:{url:%s}};
}
`, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	p.Restore(player.Session{
		Queue: []music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}, {ID: "three", Source: "cooperative"}},
		Order: []int{0, 2, 1}, At: 0, Shuffle: true,
	})
	finishLoading(t, p, posts)
	if !p.Ready() || p.Playing() {
		t.Fatalf("固定乱序会话未能暂停恢复：error=%q", p.Err())
	}
	p.Next()
	defer func() {
		if p.Playing() || p.Loading() {
			p.TogglePlay()
		}
	}()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for p.Current().ID != "two" {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatalf("没有从 three 推进到 two：current=%+v error=%q", p.Current(), p.Err())
		}
	}
	p.ToggleShuffle()
	finishLoading(t, p, posts)
	raw, err := src.Aggregate("state", nil)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Visited []string `json:"visited"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(state.Visited) != "[one three two one]" || !p.Ready() || !p.Playing() || p.Current().ID != "one" || p.Err() != "" || len(p.Queue()) != 3 {
		t.Fatalf("重排丢失失败位置记录：visited=%v current=%+v ready=%v error=%q", state.Visited, p.Current(), p.Ready(), p.Err())
	}
}

// 退出广播会按播放顺序重建队列索引，失败记录必须跟随原位置而不是误伤另一首。
func TestCooperativeEndRadioKeepsFailedQueuePositions(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
let visited=[];
export function aggregate() { return {visited}; }
export function playbackPlan({id}) {
 visited.push(id);
 return {targetQuality:"flac",minimumQuality:"128k",candidates:[
 {id,platform:"wy",trackID:id,quality:"flac"}]};
}
export function resolveCandidate({id}) {
 if(id!=="three") throw new Error("failed:"+id);
 return {stream:{url:%s}};
}
`, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	p.Restore(player.Session{
		Queue: []music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}, {ID: "three", Source: "cooperative"}, {ID: "four", Source: "cooperative"}},
		Order: []int{2, 0, 3, 1}, At: 0, Shuffle: true,
	})
	finishLoading(t, p, posts)
	p.AttachRadio("station", func(func([]music.Track)) {})
	p.Next()
	defer func() {
		if p.Playing() || p.Loading() {
			p.TogglePlay()
		}
	}()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for p.Current().ID != "four" {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatalf("没有从 one 推进到 four：current=%+v error=%q", p.Current(), p.Err())
		}
	}
	p.EndRadio()
	finishLoading(t, p, posts)
	raw, err := src.Aggregate("state", nil)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Visited []string `json:"visited"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	q := p.Queue()
	if fmt.Sprint(state.Visited) != "[three one four three]" || !p.Ready() || !p.Playing() || p.Current().ID != "three" || p.Err() != "" || p.Radio() != "" || len(q) != 3 || q[0].ID != "three" || q[1].ID != "one" || q[2].ID != "four" {
		t.Fatalf("广播截断丢失失败位置归属：visited=%v current=%+v queue=%v error=%q", state.Visited, p.Current(), q, p.Err())
	}
}

// 同曲重复入队是两个独立位置，失败去重不能按歌曲 ID 把第二项一并跳过。
func TestCooperativeFailedDuplicateQueuePositionsStayIndependent(t *testing.T) {
	src := cooperativePlugin(t, `
let visited=[];
export function aggregate() { return {visited}; }
export function playbackPlan({id}) {
 visited.push(id);
 return {targetQuality:"flac",minimumQuality:"128k",candidates:[
 {id,platform:"wy",trackID:id,quality:"flac"}]};
}
export function resolveCandidate({id}) { throw new Error("failed:"+id); }
`)
	p, posts := cooperativePlayer(src)
	p.PlayQueue([]music.Track{{ID: "same", Source: "cooperative", Name: "first"}, {ID: "same", Source: "cooperative", Name: "second"}}, 0)
	finishLoading(t, p, posts)
	raw, err := src.Aggregate("state", nil)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Visited []string `json:"visited"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(state.Visited) != "[same same]" || p.Playing() || p.Current().Name != "second" || p.Err() != "failed:same" || len(p.Queue()) != 2 {
		t.Fatalf("重复入队位置被错误合并：visited=%v current=%+v error=%q", state.Visited, p.Current(), p.Err())
	}
}

// 普通来源失败同样延续当前失败区间，重排后不能重试前面的聚合失败项。
func TestCooperativeShuffleKeepsFailuresAcrossLegacySource(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
let visited=[];
export function aggregate() { return {visited}; }
export function playbackPlan({id}) {
 visited.push(id);
 return {targetQuality:"flac",minimumQuality:"128k",candidates:[
 {id,platform:"wy",trackID:id,quality:"flac"}]};
}
export function resolveCandidate({id}) {
 if(id!=="one") throw new Error("failed:"+id);
 return {stream:{url:%s}};
}
`, audioPath(t, "seek.flac")))
	dir := t.TempDir()
	for name, contents := range map[string]string{
		"plugin.json": `{"id":"legacy"}`,
		"index.js":    `let visited=[]; export function aggregate() { return {visited}; } export function stream({id}) { visited.push(id); throw new Error("failed:"+id); }`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	legacy, err := plugin.Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(legacy.Close)
	p, posts := cooperativePlayer(src)
	p.Source = func(id string) music.Source {
		if id == "legacy" {
			return legacy
		}
		return src
	}
	p.SetVolume(0)
	p.Restore(player.Session{
		// 成功后的下一项仍走协作路径，避免普通来源的合法预取混入失败区间请求记录。
		Queue: []music.Track{{ID: "one", Source: "cooperative"}, {ID: "after", Source: "cooperative"}, {ID: "two", Source: "legacy"}, {ID: "three", Source: "cooperative"}},
		Order: []int{0, 3, 2, 1}, At: 0, Shuffle: true,
	})
	finishLoading(t, p, posts)
	if !p.Ready() || p.Playing() {
		t.Fatalf("固定混合队列未能暂停恢复：error=%q", p.Err())
	}
	p.Next()
	defer func() {
		if p.Playing() || p.Loading() {
			p.TogglePlay()
		}
	}()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for p.Current().ID != "two" {
		select {
		case fn := <-posts:
			fn()
		case <-timer.C:
			t.Fatalf("没有推进到普通来源：current=%+v error=%q", p.Current(), p.Err())
		}
	}
	p.ToggleShuffle()
	finishLoading(t, p, posts)
	for _, check := range []struct {
		src  *plugin.Plugin
		want string
	}{{src, "[one three one]"}, {legacy, "[two]"}} {
		raw, err := check.src.Aggregate("state", nil)
		if err != nil {
			t.Fatal(err)
		}
		var state struct {
			Visited []string `json:"visited"`
		}
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(state.Visited) != check.want {
			t.Fatalf("混合来源丢失失败区间：source=%s visited=%v want=%s", check.src.Info().ID, state.Visited, check.want)
		}
	}
	if !p.Ready() || !p.Playing() || p.Current().ID != "one" || p.Err() != "" || len(p.Queue()) != 4 {
		t.Fatalf("混合来源失败后没有继续合法位置：current=%+v error=%q", p.Current(), p.Err())
	}
}

// 两首全失败时每个队列位置只尝试一次，不能为凑默认三首阈值绕回重试。
func TestCooperativeShortQueueStopsAfterEachPositionFails(t *testing.T) {
	src := cooperativePlugin(t, `
let visited=[], candidates=[];
export function aggregate() { return {visited,candidates}; }
export function playbackPlan({id}) {
 visited.push(id);
 return {targetQuality:"flac",minimumQuality:"128k",candidates:[
 {id:"a",platform:"wy",trackID:id,quality:"flac"},
 {id:"b",platform:"tx",trackID:id,quality:"flac"}]};
}
export function resolveCandidate({id,candidate}) { candidates.push(id+":"+candidate.id); throw new Error("failed:"+id); }
`)
	p, posts := cooperativePlayer(src)
	p.PlayQueue([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	raw, err := src.Aggregate("state", nil)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Visited    []string `json:"visited"`
		Candidates []string `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(state.Visited) != "[one two]" || fmt.Sprint(state.Candidates) != "[one:a one:b two:a two:b]" || p.Playing() || p.Current().ID != "two" || p.Err() != "failed:two" || len(p.Queue()) != 2 {
		t.Fatalf("短队列绕回或候选被重复计为歌曲失败：visited=%v candidates=%v current=%+v error=%q", state.Visited, state.Candidates, p.Current(), p.Err())
	}
}

// 可调阈值按歌曲最终失败计数；候选耗尽不提前停，高于队列长度也不绕回。
func TestCooperativeQueueHonorsConfiguredFailureLimit(t *testing.T) {
	for _, tc := range []struct {
		limit   int
		want    string
		current string
	}{
		{1, "[one]", "one"},
		{2, "[one two]", "two"},
		{5, "[one two three four]", "four"},
	} {
		t.Run(fmt.Sprint(tc.limit), func(t *testing.T) {
			src := cooperativePlugin(t, fmt.Sprintf(`
let visited=[];
export function aggregate() { return {visited}; }
export function playbackPlan({id}) {
 visited.push(id);
 return {consecutiveFailureLimit:%d,targetQuality:"flac",minimumQuality:"128k",candidates:[
 {id:"a",platform:"wy",trackID:id,quality:"flac"},
 {id:"b",platform:"tx",trackID:id,quality:"flac"}]};
}
export function resolveCandidate({id}) { throw new Error("failed:"+id); }
`, tc.limit))
			p, posts := cooperativePlayer(src)
			p.PlayQueue([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}, {ID: "three", Source: "cooperative"}, {ID: "four", Source: "cooperative"}}, 0)
			finishLoading(t, p, posts)
			raw, err := src.Aggregate("state", nil)
			if err != nil {
				t.Fatal(err)
			}
			var state struct {
				Visited []string `json:"visited"`
			}
			if err := json.Unmarshal(raw, &state); err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(state.Visited) != tc.want || p.Playing() || p.Current().ID != tc.current || p.Err() != "failed:"+tc.current || len(p.Queue()) != 4 {
				t.Fatalf("可调阈值未按最终失败歌曲生效：limit=%d visited=%v current=%+v error=%q", tc.limit, state.Visited, p.Current(), p.Err())
			}
		})
	}
}

// 其他项成功后只在正常顺序再次轮到时重试失败项，不能立即跳回或永久屏蔽。
func TestCooperativeFailedPositionRetriesAfterNormalQueueCycle(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
let visited=[], first=0;
export function aggregate() { return {visited}; }
export function playbackPlan({id}) {
 visited.push(id);
 if(id==="one") first++;
 return {consecutiveFailureLimit:2,targetQuality:"flac",minimumQuality:"128k",candidates:[
 {id,platform:"wy",trackID:id,quality:"flac"}]};
}
export function resolveCandidate({id}) {
 if(id==="one" && first===1 || id==="three") throw new Error("failed:"+id);
 return {stream:{url:%s}};
}
`, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.SetVolume(0)
	p.CycleRepeat()
	p.PlayQueue([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}, {ID: "three", Source: "cooperative"}, {ID: "four", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	defer func() {
		if p.Playing() || p.Loading() {
			p.TogglePlay()
		}
	}()
	if !p.Ready() || !p.Playing() || p.Current().ID != "two" {
		t.Fatalf("没有在下一项成功开播：current=%+v error=%q", p.Current(), p.Err())
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for _, next := range []string{"four", "one"} {
		p.Seek(p.Duration())
		for p.Current().ID != next || !p.Ready() || p.Loading() {
			select {
			case fn := <-posts:
				fn()
			case <-timer.C:
				t.Fatalf("成功后未按正常顺序轮转到 %s：current=%+v error=%q", next, p.Current(), p.Err())
			}
		}
	}
	raw, err := src.Aggregate("state", nil)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Visited []string `json:"visited"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(state.Visited) != "[one two three four one]" || !p.Playing() || p.Err() != "" || len(p.Queue()) != 4 {
		t.Fatalf("失败项被立即重试或永久屏蔽：visited=%v current=%+v error=%q", state.Visited, p.Current(), p.Err())
	}
}

// 获取计划本身失败也属于本项最终失败，不能绕过默认连续三首限制。
func TestCooperativePlanFailuresStopAfterThreeTracks(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"error", `throw new Error("plan failed:"+id);`},
		{"nil", `return null;`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan({id}) { %s }
export function resolveCandidate() { throw new Error("unexpected resolve"); }
`, tc.body))
			p, posts := cooperativePlayer(src)
			p.PlayQueue([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}, {ID: "three", Source: "cooperative"}, {ID: "four", Source: "cooperative"}}, 0)
			finishLoading(t, p, posts)
			if p.Playing() || p.Current().ID != "three" || p.Err() == "" {
				t.Fatalf("plan failure bypassed failure limit: current=%+v playing=%v error=%q", p.Current(), p.Playing(), p.Err())
			}
		})
	}
}

// 旧队列缺失可靠录音身份时必须停留并提示重选，不能当作歌曲不可用而自动跳过。
func TestAggregateUnknownRecordingStaysOnQueuedTrack(t *testing.T) {
	dir := os.Getenv("MM_AGGREGATE_PLUGIN")
	if dir == "" {
		t.Skip("设置 MM_AGGREGATE_PLUGIN 后运行真实聚合播放器验收")
	}
	for _, id := range []string{
		"old-track-id",
		"aggregate:{",
		`aggregate:{"version":0,"sources":[]}`,
		`aggregate:{"version":1,"identity":{"platform":"wy","accountID":"a","trackID":"missing"},"sources":[{"platform":"tx","accountID":"b","trackID":"one"}]}`,
	} {
		t.Run(id, func(t *testing.T) {
			src, err := plugin.Load(dir, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(src.Close)
			p, posts := cooperativePlayer(src)
			queue := []music.Track{{ID: id, Source: src.Info().ID, Name: "old"}, {ID: "next-old-id", Source: src.Info().ID, Name: "next"}}
			p.PlayQueue(queue, 0)
			finishLoading(t, p, posts)
			if p.Loading() || p.Playing() || p.Ready() || p.Current().ID != id || p.Err() != "聚合队列缺少可靠的版本信息，请重新选择曲目" {
				t.Fatalf("旧身份项被跳过或缺少重选提示：current=%+v ready=%v playing=%v error=%q", p.Current(), p.Ready(), p.Playing(), p.Err())
			}
			got := p.Queue()
			if len(got) != 2 || got[0].ID != id || got[1].ID != "next-old-id" || got[0].PlaybackID != "" {
				t.Fatalf("版本不明时改变了原始队列：%+v", got)
			}
		})
	}
}

// 资格校验明确报告全局封锁时立即终止，不能跳过该候选后继续解析其他来源。
func TestCooperativeBlockedDuringCandidateValidationStopsRound(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
let visited=[], checked=[], resolved=[];
export function aggregate() { return {visited,checked,resolved}; }
export function playbackPlan({id}) {
 visited.push(id);
 return {targetQuality:"flac",minimumQuality:"128k",candidates:[
 {id:"a",platform:"wy",trackID:id,quality:"flac"},
 {id:"b",platform:"tx",trackID:id,quality:"flac"}]};
}
export function candidateValid({candidate}) {
 checked.push(candidate.id);
 if(candidate.id==="a") throw Object.assign(new Error("key revoked"),{code:"playbackBlocked"});
 return true;
}
export function resolveCandidate({candidate}) { resolved.push(candidate.id); return {stream:{url:%s}}; }
`, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	var blocked error
	var failedSource string
	failures := 0
	p.Failed = func(source string, err error) {
		failures++
		failedSource, blocked = source, err
	}
	p.SetVolume(0)
	p.PlayQueue([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	defer func() {
		if p.Playing() || p.Loading() {
			p.TogglePlay()
		}
	}()
	raw, err := src.Aggregate("state", nil)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Visited  []string `json:"visited"`
		Checked  []string `json:"checked"`
		Resolved []string `json:"resolved"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if p.Ready() || p.Playing() || p.Current().ID != "one" || p.Err() == "" || failures != 1 || failedSource != "cooperative" || !errors.Is(blocked, music.ErrPlaybackBlocked) || len(p.Queue()) != 2 || fmt.Sprint(state.Visited) != "[one]" || fmt.Sprint(state.Checked) != "[a]" || len(state.Resolved) != 0 {
		t.Fatalf("资格校验全局封锁后仍尝试来源或推进队列：visited=%v checked=%v resolved=%v current=%+v ready=%v error=%q", state.Visited, state.Checked, state.Resolved, p.Current(), p.Ready(), p.Err())
	}
}

// 明确的全局密钥封锁不是单曲失败，自动队列也必须停在当前项。
func TestCooperativeBlockedStopsQueue(t *testing.T) {
	src := cooperativePlugin(t, `
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidates:[{id:"a",platform:"wy",trackID:"a",quality:"flac"}]}; }
export function resolveCandidate() { throw Object.assign(new Error("key revoked"), {code:"playbackBlocked"}); }
`)
	p, posts := cooperativePlayer(src)
	p.PlayQueue([]music.Track{{ID: "one", Source: "cooperative"}, {ID: "two", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if p.Playing() || p.Ready() || p.Current().ID != "one" || p.Err() == "" {
		t.Fatalf("global block advanced queue: current=%+v playing=%v error=%q", p.Current(), p.Playing(), p.Err())
	}
}

// 拿到可解码媒体不是成功；主线程排队超过原候选截止也不得迟到开播。
func TestCooperativeExpiredCommitContinuesNextCandidate(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidateTimeoutMs:40,roundTimeoutMs:1000,candidates:[
{id:"a",platform:"wy",trackID:"a",quality:"flac"},{id:"b",platform:"tx",trackID:"b",quality:"flac"}]}; }
export function resolveCandidate({candidate}) {
 if (candidate.id==="b") throw new Error("next candidate reached");
 return {stream:{url:%s,type:"flac"}};
}`, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	var commit func()
	select {
	case commit = <-posts:
	case <-time.After(2 * time.Second):
		t.Fatal("candidate did not prepare")
	}
	time.Sleep(80 * time.Millisecond)
	commit()
	finishLoading(t, p, posts)
	if p.Ready() || p.Playing() || p.Err() != "next candidate reached" {
		t.Fatalf("expired candidate started: ready=%v playing=%v error=%q", p.Ready(), p.Playing(), p.Err())
	}
}

// 主线程回调已经进入后仍可能超时，音量准备耗尽预算不能继续安装并播放。
func TestCooperativeStartupDeadlineDoesNotPlayLate(t *testing.T) {
	src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidateTimeoutMs:40,roundTimeoutMs:1000,candidates:[
{id:"a",platform:"wy",trackID:"a",quality:"flac"},{id:"b",platform:"tx",trackID:"b",quality:"flac"}]}; }
export function resolveCandidate({candidate}) {
 if(candidate.id==="b") throw new Error("next candidate reached");
 return {stream:{url:%s}};
}
`, audioPath(t, "seek.flac")))
	p, posts := cooperativePlayer(src)
	p.Normalize = func() bool {
		// 只压缩等待到毫秒级；音频输出常驻 goroutine 不能放进 synctest bubble。
		time.Sleep(80 * time.Millisecond)
		return false
	}
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if p.Ready() || p.Playing() || p.Stream() != nil || p.Err() != "next candidate reached" {
		t.Fatalf("startup exceeded deadline but played: ready=%v playing=%v stream=%+v error=%q", p.Ready(), p.Playing(), p.Stream(), p.Err())
	}
}

// 换来源不重置整轮时钟，多个慢候选最多共享原二十秒。
func TestCooperativeRoundBudgetDoesNotRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := cooperativePlugin(t, `
export function playbackPlan(){return {targetQuality:"flac",minimumQuality:"128k",candidateTimeoutMs:5000,roundTimeoutMs:20000,candidates:Array.from({length:6},(_,i)=>({id:String(i),platform:"wy",trackID:String(i),quality:"flac"}))};}
export function resolveCandidate({signal}) { return new Promise((resolve, reject) => signal.addEventListener("abort", () => reject(new Error("aborted")), {once:true})); }
`)
		p, posts := cooperativePlayer(src)
		started := time.Now()
		p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
		timer := time.NewTimer(25 * time.Second)
		defer timer.Stop()
		for p.Loading() {
			select {
			case fn := <-posts:
				fn()
			case <-timer.C:
				t.Fatal("round budget restarted")
			}
		}
		if p.Ready() || p.Playing() || p.Err() == "" || time.Since(started) != 20*time.Second {
			t.Fatalf("round elapsed=%v ready=%v error=%q", time.Since(started), p.Ready(), p.Err())
		}
	})
}

// Cooperative timeout ends loading without leaking the raw context error to the listener.
func TestCooperativeTimeoutUsesRecoverablePlaybackError(t *testing.T) {
	src := cooperativePlugin(t, `
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidateTimeoutMs:10,roundTimeoutMs:20,candidates:[{id:"a",platform:"wy",trackID:"a",quality:"flac"}]}; }
export function resolveCandidate({signal}) { return new Promise((resolve, reject) => signal.addEventListener("abort", () => reject(new Error("aborted")), {once:true})); }
`)
	p, posts := cooperativePlayer(src)
	p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative"}}, 0)
	finishLoading(t, p, posts)
	if p.Loading() || p.Playing() || p.Err() != "Playback timed out; try again" {
		t.Fatalf("协作超时没有收敛为可恢复错误：loading=%v playing=%v error=%q", p.Loading(), p.Playing(), p.Err())
	}
}

// 首个解析永不返回时，候选五秒截止后在同一轮继续，不能卡死或重试该组合。
func TestCooperativeCandidateTimeoutUsesVirtualTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan() { return {targetQuality:"flac",minimumQuality:"128k",candidateTimeoutMs:5000,roundTimeoutMs:20000,candidates:[
{id:"a",platform:"wy",trackID:"a",quality:"flac"},
{id:"b",platform:"tx",trackID:"b",quality:"flac"}]}; }
export function resolveCandidate({candidate,signal}) {
 if(candidate.id==="a") return new Promise((resolve,reject)=>signal.addEventListener("abort",()=>reject(new Error("aborted")),{once:true}));
 throw new Error("second candidate reached"); // %s
}
`, audioPath(t, "seek.flac")))
		p, posts := cooperativePlayer(src)
		start := time.Now()
		p.PlayTrack([]music.Track{{ID: "one", Source: "cooperative", Duration: 1000}}, 0)
		timer := time.NewTimer(25 * time.Second)
		defer timer.Stop()
		for p.Loading() {
			select {
			case fn := <-posts:
				fn()
			case <-timer.C:
				t.Fatal("candidate did not expire within round budget")
			}
		}
		if p.Ready() || p.Err() != "second candidate reached" || time.Since(start) != 5*time.Second {
			t.Fatalf("candidate budget not shared: ready=%v err=%q elapsed=%v", p.Ready(), p.Err(), time.Since(start))
		}

	})
}
