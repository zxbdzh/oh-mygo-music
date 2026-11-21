package plugin

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"mygo-music/internal/music"
)

// 真正安装的聚合插件必须能被宿主加载，且不会把只读列表误报成云端写入能力。
func TestAggregatePluginLoadsReadOnly(t *testing.T) {
	p := loadAggregate(t)
	account, err := p.Account()
	if err != nil {
		t.Fatal(err)
	}
	if account != nil {
		t.Fatalf("合成插件不能虚构已登录的云端账号: %+v", account)
	}
	// 扫码和退出已接入；只读约束针对云端内容写入，不应隐藏有效账号入口。
	if !p.Can("signIn") || !p.Can("signOut") {
		t.Fatal("已接入的账号能力必须在来源设置中提供登录与退出操作")
	}
	page, err := p.Home()
	if err != nil {
		t.Fatal(err)
	}
	if page.Title == "" {
		t.Fatal("聚合入口没有可显示的标题")
	}
	if p.Can("playbackPlan") && p.Can("resolveCandidate") {
		id := `aggregate:{"version":1,"identity":{"platform":"wy","accountID":"a","trackID":"one"},"sources":[{"platform":"wy","accountID":"a","trackID":"one"}]}`
		plan, err := p.PlaybackPlan(context.Background(), id, "flac", "round-setup")
		if err != nil || plan == nil || len(plan.Candidates) != 0 {
			t.Fatalf("没有支持声明的来源不应虚构可尝试的音质: plan=%+v, err=%v", plan, err)
		}
		// 未经接入的解析能力必须明确报设置缺失，不能伪造可播放地址。
		_, err = p.ResolveCandidate(context.Background(), id, "round-setup", music.PlaybackCandidate{Platform: "wy", AccountID: "a", TrackID: "one", Quality: "flac"})
		if !errors.Is(err, music.ErrSetup) {
			t.Fatalf("未验证平台没有明确阻止解析: %v", err)
		}
	}
	for _, cap := range []string{"like", "createPlaylist", "addToPlaylist", "removeFromPlaylist"} {
		if p.Can(cap) {
			t.Fatalf("只读聚合不应提供云端写入能力 %s", cap)
		}
	}
}

// 设置联合更新必须原子校验，非法的最低音质不能悄悄覆盖先前有效设置。
func TestAggregatePluginRejectsInvalidSettingsAtomically(t *testing.T) {
	p := loadAggregate(t)
	if _, err := p.Aggregate("configure", map[string]any{"targetQuality": "320k", "minimumQuality": "256k"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Aggregate("configure", map[string]any{"minimumQuality": "flac"}); err == nil {
		t.Fatal("目标低于最低质量的设置应被拒绝")
	}
	raw, err := p.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Settings struct{ TargetQuality, MinimumQuality string }
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Settings.TargetQuality != "320k" || got.Settings.MinimumQuality != "256k" {
		t.Fatalf("非法更新覆盖了有效设置: %s", raw)
	}
}

// 检测等待也计入恢复总预算；平台顺序或参数非法时整笔更新均不落盘。
func TestAggregatePluginRejectsImpossibleTiming(t *testing.T) {
	p := loadAggregate(t)
	if _, err := p.Aggregate("configure", map[string]any{"stallTimeoutMs": 25000, "recoveryTimeoutMs": 40000}); err != nil {
		t.Fatal(err)
	}
	for _, update := range []map[string]any{
		{"recoveryTimeoutMs": 20000}, {"candidateTimeoutMs": -1}, {"consecutiveFailureLimit": 0}, {"refreshIntervalMs": 0},
		{"platformOrder": []string{"kg", "kg", "mg", "qs", "tx", "wy"}},
	} {
		if _, err := p.Aggregate("configure", update); err == nil {
			t.Fatalf("不可能的设置未被拒绝: %v", update)
		}
	}
	raw, err := p.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Settings struct{ StallTimeoutMs, RecoveryTimeoutMs int }
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Settings.StallTimeoutMs != 25000 || got.Settings.RecoveryTimeoutMs != 40000 {
		t.Fatalf("非法更新破坏原时间设置: %s", raw)
	}
}

// 解析密钥不能进入普通设置或状态响应，临时值关闭插件后即丢弃。
func TestAggregatePluginResolverKeyNeverBecomesAnOrdinarySetting(t *testing.T) {
	p := loadAggregate(t)
	const secret = "fixture-resolver-value"
	if _, err := p.Aggregate("configure", map[string]any{"resolverKey": secret}); err == nil {
		t.Fatal("普通设置允许写入密钥")
	}
	raw, err := p.Aggregate("setResolverKey", map[string]any{"value": secret, "temporary": true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("公开状态泄露密钥")
	}
	p.Close()
	p2, err := Load(p.dir, filepath.Dir(p.data))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p2.Close)
	raw, err = p2.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), `"state":"temporary"`) {
		t.Fatalf("本次临时密钥被持久化: %s", raw)
	}
}

// 没有完成全部分页的读取绝不能替换上一份成功快照或成功时间。
func TestAggregatePluginCommitsOnlyCompleteSnapshot(t *testing.T) {
	p := loadAggregate(t)
	first := map[string]any{"platform": "wy", "accountID": "account-a", "listID": "liked", "complete": true, "refreshedAt": 100, "tracks": []any{map[string]any{"id": "song-1", "name": "Track one"}}}
	if _, err := p.Aggregate("commitSnapshot", first); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []map[string]any{
		{"platform": "wy", "accountID": "account-a", "listID": "liked", "complete": false, "refreshedAt": 200, "tracks": []any{}},
		{"platform": "wy", "accountID": "account-a", "listID": "liked", "complete": true, "refreshedAt": -1, "tracks": []any{}},
		{"platform": "unknown", "accountID": "account-a", "listID": "liked", "complete": true, "refreshedAt": 200, "tracks": []any{}},
		{"platform": "wy", "accountID": "account-a", "listID": "liked", "complete": true, "refreshedAt": 200, "tracks": []any{map[string]any{"name": "missing identity"}}},
	} {
		if _, err := p.Aggregate("commitSnapshot", invalid); err == nil {
			t.Fatalf("非法或不完整刷新被接受: %+v", invalid)
		}
	}
	raw, err := p.Aggregate("snapshot", map[string]any{"platform": "wy", "accountID": "account-a", "listID": "liked"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		RefreshedAt int
		Tracks      []struct{ ID string }
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.RefreshedAt != 100 || len(got.Tracks) != 1 || got.Tracks[0].ID != "song-1" {
		t.Fatalf("失败刷新覆盖旧快照: %s", raw)
	}
}

// 快照按平台账号及列表三重隔离，同一列表提交不会覆盖另一个账号的数据。
// 主动退出只清除所属账号的数据，且迟到的旧世代完整回包不能恢复快照。
func TestAggregatePluginLogoutClearsOnlyItsAccountAndFencesLateCommit(t *testing.T) {
	p := loadAggregate(t)
	commit := func(account string, generation int, track string) error {
		_, err := p.Aggregate("commitSnapshot", map[string]any{"platform": "wy", "accountID": account, "listID": "liked", "generation": generation, "complete": true, "refreshedAt": 10, "tracks": []any{map[string]any{"id": track}}})
		return err
	}
	for _, account := range []string{"a", "b"} {
		generation, err := p.Aggregate("snapshotGeneration", map[string]any{"platform": "wy", "accountID": account})
		if err != nil {
			t.Fatal(err)
		}
		var v struct{ Generation int }
		if err := json.Unmarshal(generation, &v); err != nil {
			t.Fatal(err)
		}
		if err := commit(account, v.Generation, account+"-song"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Aggregate("logout", map[string]any{"platform": "wy", "accountID": "a"}); err != nil {
		t.Fatal(err)
	}
	if err := commit("a", 0, "late-song"); err == nil {
		t.Fatal("退出前发起的旧回包恢复了快照")
	}
	if _, err := p.Aggregate("commitSnapshot", map[string]any{"platform": "wy", "accountID": "a", "listID": "liked", "complete": true, "refreshedAt": 20, "tracks": []any{map[string]any{"id": "generation-less-late-song"}}}); err == nil {
		t.Fatal("未携带账号世代的迟到回包绕过了退出栅栏")
	}
	a, err := p.Aggregate("snapshot", map[string]any{"platform": "wy", "accountID": "a", "listID": "liked"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Aggregate("snapshot", map[string]any{"platform": "wy", "accountID": "b", "listID": "liked"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(a), "late-song") || !strings.Contains(string(b), "b-song") {
		t.Fatalf("退出清理越界或留下迟到数据: a=%s b=%s", a, b)
	}
}

// 退出清除账号快照后，旧队列仍在，但其播放计划不能再携带该账号来源。
func TestAggregatePluginOldQueueStopsUsingLoggedOutAccount(t *testing.T) {
	p := loadAggregate(t)
	left := music.AggregateSource{Platform: "wy", AccountID: "a", TrackID: "wy-one"}
	right := music.AggregateSource{Platform: "tx", AccountID: "b", TrackID: "tx-one"}
	for _, source := range []music.AggregateSource{left, right} {
		if _, err := p.Aggregate("commitSnapshot", map[string]any{"platform": source.Platform, "accountID": source.AccountID, "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": source.TrackID, "name": "Recording", "qualities": []string{"flac"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Aggregate("confirmLink", map[string]any{"left": left, "right": right}); err != nil {
		t.Fatal(err)
	}
	page, err := p.Home()
	if err != nil || len(page.Sections[0].Tracks) != 1 {
		t.Fatalf("测试曲目没有建立人工关联: %+v %v", page.Sections, err)
	}
	queuedID := page.Sections[0].Tracks[0].ID
	if _, err := p.Aggregate("logout", map[string]any{"platform": "wy", "accountID": "a", "clearCredential": true}); err != nil {
		t.Fatal(err)
	}
	plan, err := p.PlaybackPlan(context.Background(), queuedID, "flac", "after-logout")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Platform != "tx" || plan.Candidates[0].AccountID != "b" {
		t.Fatalf("退出账号仍出现在旧队列候选中: %+v", plan.Candidates)
	}
}

// 来源歌单管理只枚举完整本机快照的元数据，不能泄露曲目或伪造云端列表。
func TestAggregatePluginListsCachedPlaylistSnapshots(t *testing.T) {
	p := loadAggregate(t)
	for _, snapshot := range []map[string]any{
		{"platform": "wy", "accountID": "account-a", "listID": "liked", "listName": "Liked", "complete": true, "refreshedAt": 100, "selected": true, "tracks": []any{map[string]any{"id": "song-1", "name": "Track one"}}},
		{"platform": "wy", "accountID": "account-a", "listID": "playlist-1", "listName": "Road songs", "complete": true, "refreshedAt": 200, "tracks": []any{map[string]any{"id": "song-2", "name": "Track two"}}},
	} {
		if _, err := p.Aggregate("commitSnapshot", snapshot); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := p.Aggregate("lists", nil)
	if err != nil {
		t.Fatal(err)
	}
	var lists []struct {
		Platform, AccountID, ListID, ListName string
		RefreshedAt                           int64
		Selected                              bool
	}
	if err := json.Unmarshal(raw, &lists); err != nil {
		t.Fatal(err)
	}
	if len(lists) != 2 || lists[0].ListID != "liked" || !lists[0].Selected || lists[1].ListID != "playlist-1" || lists[1].Selected || strings.Contains(string(raw), "Track one") || strings.Contains(string(raw), "song-2") {
		t.Fatalf("缓存列表目录缺少筛选状态或泄露曲目: %s", raw)
	}
	selected, err := p.Aggregate("selectList", map[string]any{"platform": "wy", "accountID": "account-a", "listID": "playlist-1", "selected": true})
	if err != nil || !strings.Contains(string(selected), `"selected":true`) {
		t.Fatalf("不能纳入已缓存的完整来源歌单: %s, %v", selected, err)
	}
	if _, err := p.Aggregate("selectList", map[string]any{"platform": "wy", "accountID": "account-a", "listID": "missing", "selected": true}); !errors.Is(err, music.ErrUnavailable) {
		t.Fatalf("不完整/未知列表被允许纳入: %v", err)
	}
	page, err := p.Home()
	if err != nil || len(page.Sections[0].Tracks) != 2 {
		t.Fatalf("纳入列表没有进入只读聚合首页: %+v %v", page.Sections, err)
	}
	if _, err := p.Aggregate("selectList", map[string]any{"platform": "wy", "accountID": "account-a", "listID": "playlist-1", "selected": false}); err != nil {
		t.Fatal(err)
	}
	page, err = p.Home()
	if err != nil || len(page.Sections[0].Tracks) != 1 {
		t.Fatalf("移出列表删除了喜欢快照或未更新聚合首页: %+v %v", page.Sections, err)
	}
}

// 人工关联跨重启保留；撤销形成长期排除，只有用户再次确认才重新合并。
func TestAggregatePluginManualLinkRevocationPersistsUntilReconfirmed(t *testing.T) {
	p := loadAggregate(t)
	left := map[string]any{"platform": "wy", "accountID": "a", "trackID": "raw:01"}
	right := map[string]any{"platform": "tx", "accountID": "b", "trackID": "01"}
	input := map[string]any{"left": left, "right": right}
	if _, err := p.Aggregate("confirmLink", input); err != nil {
		t.Fatal(err)
	}
	raw, err := p.Aggregate("association", input)
	if err != nil {
		t.Fatal(err)
	}
	var grouped struct {
		SameRecording bool
		Manual        bool
	}
	if err := json.Unmarshal(raw, &grouped); err != nil {
		t.Fatal(err)
	}
	if !grouped.SameRecording || !grouped.Manual {
		t.Fatalf("人工确认没有合并: %s", raw)
	}
	p.Close()
	p2, err := Load(p.dir, filepath.Dir(p.data))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p2.Aggregate("revokeLink", input); err != nil {
		t.Fatal(err)
	}
	p2.Close()
	p3, err := Load(p.dir, filepath.Dir(p.data))
	if err != nil {
		t.Fatal(err)
	}
	defer p3.Close()
	raw, err = p3.Aggregate("association", input)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &grouped); err != nil {
		t.Fatal(err)
	}
	if grouped.SameRecording {
		t.Fatalf("跨重启撤销后仍然合并: %s", raw)
	}
	if _, err := p3.Aggregate("confirmLink", input); err != nil {
		t.Fatal(err)
	}
	raw, err = p3.Aggregate("association", input)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &grouped); err != nil {
		t.Fatal(err)
	}
	if !grouped.SameRecording || !grouped.Manual {
		t.Fatalf("重新确认没有解除排除: %s", raw)
	}
}

// 本机相同账号字符串在不同平台仍是两个隔离账号。
func TestAggregatePluginPlaybackPlanUsesConfiguredBudgetsAndHonorsQualityFloor(t *testing.T) {
	p := loadAggregate(t)
	for _, source := range []music.AggregateSource{{Platform: "wy", AccountID: "a", TrackID: "song-flac"}, {Platform: "tx", AccountID: "b", TrackID: "song-320k"}} {
		if _, err := p.Aggregate("commitSnapshot", map[string]any{"platform": source.Platform, "accountID": source.AccountID, "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": source.TrackID, "name": "Song", "qualities": []string{"flac"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := p.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections[0].Tracks) != 2 {
		t.Fatalf("缺少质量测试来源: %+v", page.Sections)
	}
	if _, err := p.Aggregate("configure", map[string]any{"minimumQuality": "320k"}); err != nil {
		t.Fatal(err)
	}
	var id string
	for _, track := range page.Sections[0].Tracks {
		if track.AggregateSources[0].Platform == "wy" {
			id = track.ID
		}
	}
	plan, err := p.PlaybackPlan(context.Background(), id, "flac", "round-quality")
	if err != nil {
		t.Fatal(err)
	}
	if plan.TargetQuality != "flac" || plan.MinimumQuality != "320k" || plan.CandidateTimeoutMs != 5000 || plan.RoundTimeoutMs != 20000 {
		t.Fatalf("播放计划没有冻结设置预算: %+v", plan)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Platform != "wy" || plan.Candidates[0].Quality != "flac" {
		t.Fatalf("计划未保留原始来源声明或伪造高于下限的证据: %+v", plan.Candidates)
	}
}

func TestAggregatePluginRejectsUntrustedIdentityValues(t *testing.T) {
	p := loadAggregate(t)
	for _, input := range []map[string]any{
		{"platform": "unknown", "accountID": "a", "listID": "liked"},
		{"platform": "wy", "accountID": "", "listID": "liked"},
		{"platform": "wy", "accountID": "a", "listID": ""},
	} {
		if _, err := p.Aggregate("snapshot", input); err == nil {
			t.Fatalf("无效快照身份未拒绝: %+v", input)
		}
	}
	for _, input := range []map[string]any{
		{"platform": "unknown", "accountID": "a"},
		{"platform": "wy", "accountID": ""},
	} {
		if _, err := p.Aggregate("logout", input); err == nil {
			t.Fatalf("无效退出身份未拒绝: %+v", input)
		}
	}
	if _, err := p.Aggregate("association", map[string]any{"left": map[string]any{"platform": "wy", "accountID": "a", "trackID": "1"}, "right": map[string]any{"platform": "foreign", "accountID": "b", "trackID": "2"}}); err == nil {
		t.Fatal("未验证的平台身份进入人工关系")
	}
}

func TestAggregatePluginLogoutPreservesSameNamedCredentialOnAnotherPlatform(t *testing.T) {
	store := &memoryCredentials{values: map[string]string{}}
	previous := systemCredentials
	systemCredentials = store
	t.Cleanup(func() { systemCredentials = previous })
	p := loadAggregate(t)
	for _, platform := range []string{"wy", "tx"} {
		if _, err := p.Aggregate("commitSnapshot", map[string]any{"platform": platform, "accountID": "same-id", "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": platform}}}); err != nil {
			t.Fatal(err)
		}
		key := p.credentialTarget(`platform:["` + platform + `","same-id"]`)
		if err := store.set(key, platform+"-cookie"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Aggregate("logout", map[string]any{"platform": "wy", "accountID": "same-id", "clearCredential": true}); err != nil {
		t.Fatal(err)
	}
	wy, wyFound, err := store.get(p.credentialTarget(`platform:["wy","same-id"]`))
	if err != nil {
		t.Fatal(err)
	}
	tx, txFound, err := store.get(p.credentialTarget(`platform:["tx","same-id"]`))
	if err != nil {
		t.Fatal(err)
	}
	if wyFound || wy != "" || !txFound || tx != "tx-cookie" {
		t.Fatalf("退出一个平台误清或保留了错误凭据: wy=%q found=%v tx=%q found=%v", wy, wyFound, tx, txFound)
	}
}

func TestAggregatePluginLogoutPreservesSameNamedAccountOnAnotherPlatform(t *testing.T) {
	p := loadAggregate(t)
	for _, platform := range []string{"wy", "tx"} {
		if _, err := p.Aggregate("commitSnapshot", map[string]any{"platform": platform, "accountID": "same-id", "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": platform}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Aggregate("logout", map[string]any{"platform": "wy", "accountID": "same-id"}); err != nil {
		t.Fatal(err)
	}
	page, err := p.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections[0].Tracks) != 1 || page.Sections[0].Tracks[0].AggregateSources[0].Platform != "tx" {
		t.Fatalf("退出一个平台账号删除了另一个平台的同名账号: %+v", page.Sections)
	}
}

// 大缓存首页只查已有的关联，且仍识别升级前保存的关联键。
func TestAggregatePluginHomeSkipsUnrecordedPairs(t *testing.T) {
	p := loadAggregateWithPrelude(t, `
const readHomeStorage = host.storage.get;
let homeLinkReads = 0;
host.storage.get = key => {
  if (key.startsWith("link:") && ++homeLinkReads > 20) throw new Error("Home scanned unrecorded pairs");
  return readHomeStorage(key);
};
`)
	p.mu.Lock()
	p.storage[`link:["[\"tx\",\"qa\",\"0\"]","[\"tx\",\"qa\",\"999\"]"]`] = json.RawMessage(`true`)
	p.mu.Unlock()
	tracks := make([]map[string]string, 1000)
	for index := range tracks {
		id := strconv.Itoa(index)
		tracks[index] = map[string]string{"id": id, "name": "Cached " + id}
	}
	if _, err := p.Aggregate("commitSnapshot", map[string]any{
		"platform": "tx", "accountID": "qa", "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": tracks,
	}); err != nil {
		t.Fatal(err)
	}
	page, err := p.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections) != 1 || len(page.Sections[0].Tracks) != 999 {
		t.Fatal("首页遗漏曲目或未合并已有的人工关联")
	}
	first := page.Sections[0].Tracks[0]
	if first.Name != "Cached 0" || len(first.AggregateLinks) != 1 || len(first.AggregateSources) != 2 {
		t.Fatal("首页未保留顺序、旧关联或原始来源")
	}
}

// Home 保留同曲目的所有列表来源，同时让同名但未确认的跨平台录音分开。
func TestAggregatePluginHomeShowsSnapshotSourcesAndManualMerge(t *testing.T) {
	p := loadAggregate(t)
	for _, snapshot := range []map[string]any{
		{"platform": "wy", "accountID": "a", "listID": "liked", "complete": true, "refreshedAt": 11, "tracks": []any{map[string]any{"id": "raw-01", "name": "Song one"}}},
		{"platform": "wy", "accountID": "a", "listID": "selected", "selected": true, "complete": true, "refreshedAt": 12, "tracks": []any{map[string]any{"id": "raw-01", "name": "Song one"}}},
		{"platform": "tx", "accountID": "b", "listID": "liked", "complete": true, "refreshedAt": 13, "tracks": []any{map[string]any{"id": "01", "name": "Song one"}}},
	} {
		if _, err := p.Aggregate("commitSnapshot", snapshot); err != nil {
			t.Fatal(err)
		}
	}
	page, err := p.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections) != 1 || len(page.Sections[0].Tracks) != 2 {
		t.Fatalf("跨平台同名被误合并或快照遗漏: %+v", page.Sections)
	}
	var localFound bool
	for _, track := range page.Sections[0].Tracks {
		if track.Name == "Song one" && len(track.AggregateSources) == 2 {
			localFound = true
		}
	}
	if !localFound {
		t.Fatalf("同平台曲目没有保留喜欢和选定歌单两个来源: %+v", page.Sections[0].Tracks)
	}
	link := map[string]any{"left": map[string]any{"platform": "wy", "accountID": "a", "trackID": "raw-01"}, "right": map[string]any{"platform": "tx", "accountID": "b", "trackID": "01"}}
	if _, err := p.Aggregate("confirmLink", link); err != nil {
		t.Fatal(err)
	}
	page, err = p.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections[0].Tracks) != 1 || len(page.Sections[0].Tracks[0].AggregateSources) != 3 || !page.Sections[0].Tracks[0].AggregateManual {
		t.Fatalf("人工关联没有保留来源并合并: %+v", page.Sections)
	}
	if len(page.Sections[0].Tracks[0].AggregateLinks) != 1 {
		t.Fatalf("人工关系边没有随曲目公开: %+v", page.Sections[0].Tracks[0].AggregateLinks)
	}
	gotLink := page.Sections[0].Tracks[0].AggregateLinks[0]
	if !((gotLink.Left.TrackID == "raw-01" && gotLink.Right.TrackID == "01") || (gotLink.Left.TrackID == "01" && gotLink.Right.TrackID == "raw-01")) {
		t.Fatalf("人工关系边身份不对: %+v", gotLink)
	}
	if page.Sections[0].Tracks[0].Source != p.Info().ID || page.Sections[0].Tracks[0].AggregateSources[0].TrackID == page.Sections[0].Tracks[0].ID {
		t.Fatalf("宿主源身份覆盖了可恢复的原始平台ID: %+v", page.Sections[0].Tracks[0])
	}
}

// 连接来源最后出现也必须合并完整人工关系，不能让快照遍历顺序决定录音分组。
func TestAggregatePluginJoinsManualLinksThroughLaterSource(t *testing.T) {
	p := loadAggregate(t)
	sources := []music.AggregateSource{
		{Platform: "kg", AccountID: "a", TrackID: "left"},
		{Platform: "tx", AccountID: "b", TrackID: "right"},
		{Platform: "wy", AccountID: "c", TrackID: "bridge"},
	}
	for _, source := range sources {
		if _, err := p.Aggregate("commitSnapshot", map[string]any{"platform": source.Platform, "accountID": source.AccountID, "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": source.TrackID, "name": "Recording", "qualities": []string{"flac"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range [][2]int{{0, 2}, {1, 2}} {
		if _, err := p.Aggregate("confirmLink", map[string]any{"left": sources[pair[0]], "right": sources[pair[1]]}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := p.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections[0].Tracks) != 1 || len(page.Sections[0].Tracks[0].AggregateSources) != 3 || len(page.Sections[0].Tracks[0].AggregateLinks) != 2 {
		t.Fatalf("最后出现的人工关联桥接没有完整合并来源: %+v", page.Sections)
	}
	plan, err := p.PlaybackPlan(context.Background(), page.Sections[0].Tracks[0].ID, "flac", "linked-round")
	if err != nil || plan == nil || len(plan.Candidates) != 3 {
		t.Fatalf("已确认的间接同录音来源不在播放候选中: plan=%+v err=%v", plan, err)
	}
	if _, err := p.Aggregate("revokeLink", map[string]any{"left": sources[0], "right": sources[1]}); err != nil {
		t.Fatal(err)
	}
	page, err = p.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections[0].Tracks) != 2 {
		t.Fatalf("持久排除被第三个已关联来源绕过: %+v", page.Sections)
	}
	for _, track := range page.Sections[0].Tracks {
		var hasLeft, hasRight bool
		for _, source := range track.AggregateSources {
			hasLeft = hasLeft || source.Platform == "kg"
			hasRight = hasRight || source.Platform == "tx"
		}
		if hasLeft && hasRight {
			t.Fatal("已撤销的一对版本被间接重新合并")
		}
	}
}

// 平台排序只决定新一轮先试哪个来源，不能改变同一个录音组的稳定版本锚点。
func TestAggregatePluginReorderingChangesCandidateOrderWithoutChangingAnchor(t *testing.T) {
	p := loadAggregate(t)
	left := music.AggregateSource{Platform: "wy", AccountID: "a", TrackID: "raw-01"}
	right := music.AggregateSource{Platform: "tx", AccountID: "b", TrackID: "01"}
	for _, source := range []music.AggregateSource{left, right} {
		if _, err := p.Aggregate("commitSnapshot", map[string]any{"platform": source.Platform, "accountID": source.AccountID, "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": source.TrackID, "qualities": []string{"flac"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Aggregate("confirmLink", map[string]any{"left": left, "right": right}); err != nil {
		t.Fatal(err)
	}
	before, err := p.Home()
	if err != nil {
		t.Fatal(err)
	}
	queuedID := before.Sections[0].Tracks[0].ID
	var frozen struct{ Identity music.AggregateSource }
	if err := json.Unmarshal([]byte(strings.TrimPrefix(queuedID, "aggregate:")), &frozen); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Aggregate("configure", map[string]any{"platformOrder": []string{"wy", "kg", "kw", "mg", "qs", "tx"}}); err != nil {
		t.Fatal(err)
	}
	after, err := p.Home()
	if err != nil {
		t.Fatal(err)
	}
	var updated struct{ Identity music.AggregateSource }
	if err := json.Unmarshal([]byte(strings.TrimPrefix(after.Sections[0].Tracks[0].ID, "aggregate:")), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Identity != frozen.Identity {
		t.Fatalf("平台偏好改变了版本锚点: before=%+v after=%+v", frozen.Identity, updated.Identity)
	}
	plan, err := p.PlaybackPlan(context.Background(), queuedID, "flac", "reordered-round")
	if err != nil || plan == nil || len(plan.Candidates) != 2 || plan.Candidates[0].Platform != "wy" || plan.Candidates[1].Platform != "tx" {
		t.Fatalf("旧队列的新一轮没有应用平台偏好: plan=%+v err=%v", plan, err)
	}
}

// 聚合项拆分后，锚点及剩余同版来源各留在自己的曲目组，不依赖平台排序。
func TestAggregatePluginHomeSplitsManualGroupsByFrozenAnchor(t *testing.T) {
	p := loadAggregate(t)
	left := music.AggregateSource{Platform: "wy", AccountID: "a", TrackID: "raw-01"}
	right := music.AggregateSource{Platform: "tx", AccountID: "b", TrackID: "01"}
	for _, source := range []music.AggregateSource{left, right} {
		if _, err := p.Aggregate("commitSnapshot", map[string]any{"platform": source.Platform, "accountID": source.AccountID, "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": source.TrackID, "name": "Song", "qualities": []string{"flac"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Aggregate("confirmLink", map[string]any{"left": left, "right": right}); err != nil {
		t.Fatal(err)
	}
	page, err := p.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections[0].Tracks) != 1 {
		t.Fatalf("人工关系没有合并: %+v", page.Sections)
	}
	id := page.Sections[0].Tracks[0].ID
	var queued struct {
		Sources []music.AggregateSource `json:"sources"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(id, "aggregate:")), &queued); err != nil {
		t.Fatal(err)
	}
	if len(queued.Sources) != 2 {
		t.Fatalf("入队锚点外缺少已确认来源: %+v", queued)
	}
	if _, err := p.Aggregate("revokeLink", map[string]any{"left": left, "right": right}); err != nil {
		t.Fatal(err)
	}
	page, err = p.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections[0].Tracks) != 2 {
		t.Fatalf("撤销后两个来源没有拆开: %+v", page.Sections)
	}
}

// 入队候选快照只包含入队时已有的来源：新增关系不扩大，撤销关系立即收紧。
func TestAggregatePluginPlaybackPlanFreezesAndOnlyRevokesCandidates(t *testing.T) {
	p := loadAggregate(t)
	left := music.AggregateSource{Platform: "wy", AccountID: "a", TrackID: "raw-01"}
	right := music.AggregateSource{Platform: "tx", AccountID: "b", TrackID: "01"}
	for _, s := range []music.AggregateSource{left, right} {
		if _, err := p.Aggregate("commitSnapshot", map[string]any{"platform": s.Platform, "accountID": s.AccountID, "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": s.TrackID, "name": "Song", "qualities": []string{"flac"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	input := map[string]any{"left": left, "right": right}
	if _, err := p.Aggregate("confirmLink", input); err != nil {
		t.Fatal(err)
	}
	page, err := p.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections[0].Tracks) != 1 {
		t.Fatalf("人工确认未构建单曲目: %+v", page.Sections)
	}
	queuedID := page.Sections[0].Tracks[0].ID
	var queued struct {
		Identity music.AggregateSource `json:"identity"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(queuedID, "aggregate:")), &queued); err != nil {
		t.Fatal(err)
	}
	anchor := queued.Identity
	plan, err := p.PlaybackPlan(context.Background(), queuedID, "flac", "round-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 2 {
		t.Fatalf("入队时已确认来源没有进入候选快照: %+v", plan.Candidates)
	}
	third := music.AggregateSource{Platform: "mg", AccountID: "c", TrackID: "03"}
	if _, err := p.Aggregate("commitSnapshot", map[string]any{"platform": third.Platform, "accountID": third.AccountID, "listID": "liked", "complete": true, "refreshedAt": 2, "tracks": []any{map[string]any{"id": third.TrackID, "name": "Song"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Aggregate("confirmLink", map[string]any{"left": left, "right": third}); err != nil {
		t.Fatal(err)
	}
	plan, err = p.PlaybackPlan(context.Background(), queuedID, "flac", "round-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 2 {
		t.Fatalf("新关联扩大了旧队列候选: %+v", plan.Candidates)
	}
	if _, err := p.Aggregate("revokeLink", input); err != nil {
		t.Fatal(err)
	}
	plan, err = p.PlaybackPlan(context.Background(), queuedID, "flac", "round-3")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Platform != anchor.Platform || plan.Candidates[0].AccountID != anchor.AccountID || plan.Candidates[0].TrackID != anchor.TrackID {
		t.Fatalf("撤销没有立即收紧至入队锚点来源: anchor=%+v candidates=%+v", anchor, plan.Candidates)
	}
}

// 平台、账号和列表共同构成快照身份，禁止跨账号读到相同列表 ID。
func TestAggregatePluginSnapshotsAreAccountScoped(t *testing.T) {
	p := loadAggregate(t)
	for _, snapshot := range []map[string]any{
		{"platform": "wy", "accountID": "a", "listID": "liked", "complete": true, "refreshedAt": 11, "tracks": []any{map[string]any{"id": "a-song"}}},
		{"platform": "wy", "accountID": "b", "listID": "liked", "complete": true, "refreshedAt": 22, "tracks": []any{map[string]any{"id": "b-song"}}},
	} {
		if _, err := p.Aggregate("commitSnapshot", snapshot); err != nil {
			t.Fatal(err)
		}
	}
	for account, want := range map[string]string{"a": "a-song", "b": "b-song"} {
		raw, err := p.Aggregate("snapshot", map[string]any{"platform": "wy", "accountID": account, "listID": "liked"})
		if err != nil {
			t.Fatal(err)
		}
		var got struct{ Tracks []struct{ ID string } }
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Tracks) != 1 || got.Tracks[0].ID != want {
			t.Fatalf("account %s received another account's tracks: %s", account, raw)
		}
	}
}

func loadAggregate(t *testing.T) *Plugin {
	t.Helper()
	return loadAggregateWithPrelude(t, "")
}

// prelude 仅用于替换测试的系统边界；默认仍加载未改写的正式插件代码。
func loadAggregateWithPrelude(t *testing.T, prelude string) *Plugin {
	t.Helper()
	dir := os.Getenv("MM_AGGREGATE_PLUGIN")
	if dir == "" {
		t.Skip("设置 MM_AGGREGATE_PLUGIN 后运行跨仓库真实插件验收")
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	main, _ := m["main"].(string)
	code, err := os.ReadFile(filepath.Join(dir, main))
	if err != nil {
		t.Fatal(err)
	}
	// 每个测试拥有独立真实 JS 插件身份，避免共享本地状态或凭据。
	m["id"] = "aggregate-test-" + strings.ToLower(rand.Text())
	m["main"] = "index.js"
	manifest, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return load(t, string(manifest), prelude+string(code))
}
