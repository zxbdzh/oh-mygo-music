package mv

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store 保存绑定和解析结果。
//
// 解析结果只存 URL、请求头和过期时间；登录凭据不在这里，
// 因为它属于系统凭据管理器，也因为它不该被写进普通数据文件。
type Store struct {
	path string

	mu   sync.Mutex
	data storeData
}

type storeData struct {
	Bindings map[string]Binding  `json:"bindings"`
	Resolved map[string]Resolved `json:"resolved"`
}

// Open 读取磁盘上的数据；文件不存在时从空开始，让首次启动不用特殊处理。
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("mv: store needs a path")
	}
	s := &Store{path: path, data: newStoreData()}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		// 损坏时保留文件、从空开始，避免把听众的旧绑定直接覆盖掉。
		s.data = newStoreData()
		return s, nil
	}
	if s.data.Bindings == nil {
		s.data.Bindings = map[string]Binding{}
	}
	if s.data.Resolved == nil {
		s.data.Resolved = map[string]Resolved{}
	}
	return s, nil
}

func newStoreData() storeData {
	return storeData{Bindings: map[string]Binding{}, Resolved: map[string]Resolved{}}
}

// Binding 取一首歌的绑定。没有时返回零值和 false。
func (s *Store) Binding(source, trackID string) (Binding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.Bindings[bindingKey(source, trackID)]
	return b, ok
}

// PutBinding 写入或替换一首歌的绑定，并立即落盘。
func (s *Store) PutBinding(b Binding) error {
	b.UpdatedAt = time.Now()
	s.mu.Lock()
	s.data.Bindings[b.Key()] = b
	// 换 MV 后旧的解析结果不再对应，顺手清掉，避免下次误用。
	delete(s.data.Resolved, b.VideoID)
	err := s.saveLocked()
	s.mu.Unlock()
	return err
}

// HasOtherSourceBinding 只用来提示：同名的歌在另一个音源已有 MV。
// 曲目 ID 跨音源不可比，所以用保存下来的歌名和歌手做弱匹配。
func (s *Store) HasOtherSourceBinding(source, name, artist string) bool {
	name, artist = normalize(name), normalize(artist)
	if name == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.data.Bindings {
		if b.Source == source || normalize(b.TrackName) != name {
			continue
		}
		if artist == "" || normalize(b.TrackArtist) == artist {
			return true
		}
	}
	return false
}

// DeleteBinding 解除绑定，并清掉该 MV 的解析缓存。
func (s *Store) DeleteBinding(source, trackID string) error {
	s.mu.Lock()
	key := bindingKey(source, trackID)
	b, ok := s.data.Bindings[key]
	delete(s.data.Bindings, key)
	if ok {
		delete(s.data.Resolved, b.VideoID)
	}
	err := s.saveLocked()
	s.mu.Unlock()
	return err
}

// Resolved 取一条仍然有效的解析结果；过期的当作没有，让调用方重新解析。
func (s *Store) Resolved(videoID string) (Resolved, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data.Resolved[videoID]
	if !ok || r.Expired(time.Now()) {
		return Resolved{}, false
	}
	return r, true
}

// PutResolved 缓存解析结果，省掉重复的 playurl 请求。
func (s *Store) PutResolved(videoID string, r Resolved) error {
	s.mu.Lock()
	s.data.Resolved[videoID] = r
	err := s.saveLocked()
	s.mu.Unlock()
	return err
}

// saveLocked 先写临时文件再改名，避免半截文件把已有数据弄坏。调用方持锁。
func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
