package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/MoeForge/moejs"

	"mygo-music/internal/credential"
)

// Windows GENERIC 凭据 blob 最多 5*512 字节，在公共边界检查以保持测试与系统一致。
const credentialBlobLimit = credential.BlobLimit

// 只允许操作系统保护的存储；测试也只在这个边界替换，不绕过真实 JS 宿主。
type credentialStore interface {
	get(target string) (string, bool, error)
	set(target, value string) error
	remove(target string) error
}

var systemCredentials credentialStore = platformCredentials{}

var (
	errCredentialsUnavailable = credential.ErrUnavailable
	errCredentialsUnsupported = credential.ErrUnsupported
	errCredentialArgument     = errors.New("credentials require a nonempty string key and a string value of at most 2560 bytes")
)

// platformCredentials 让插件测试继续通过小型接口替换系统凭据。
type platformCredentials struct{}

func (platformCredentials) get(target string) (string, bool, error) { return credential.Get(target) }
func (platformCredentials) set(target, value string) error          { return credential.Set(target, value) }
func (platformCredentials) remove(target string) error              { return credential.Remove(target) }

func (p *Plugin) credentialTarget(key string) string {
	// Windows 目标名不区分大小写，分别哈希插件和键，避免大小写及分隔符跨域碰撞。
	id := sha256.Sum256([]byte(p.manifest.ID))
	name := sha256.Sum256([]byte(key))
	return "MyGo-Music/Plugin/" + hex.EncodeToString(id[:]) + "/" + hex.EncodeToString(name[:])
}

func credentialKey(args []moejs.Value) (string, error) {
	key := moejs.Arg(args, 0)
	if !key.IsString() || key.String() == "" {
		return "", errCredentialArgument
	}
	return key.String(), nil
}

func (p *Plugin) credentialGet(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
	key, err := credentialKey(args)
	if err != nil {
		return moejs.Undefined(), err
	}
	value, found, err := systemCredentials.get(p.credentialTarget(key))
	if err != nil {
		return moejs.Undefined(), credentialError(err)
	}
	if !found {
		return moejs.Undefined(), nil
	}
	p.diagnostics.remember(value)
	return moejs.String(value), nil
}

func (p *Plugin) credentialSet(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
	key, err := credentialKey(args)
	if err != nil {
		return moejs.Undefined(), err
	}
	value := moejs.Arg(args, 1)
	// 不把 undefined、对象等隐式转成秘密，也不在校验错误中回显用户输入。
	if !value.IsString() || len(value.String()) > credentialBlobLimit {
		return moejs.Undefined(), errCredentialArgument
	}
	// 保存失败后脚本也可能记录本次输入，诊断保护不能以持久写入成功为前提。
	p.diagnostics.remember(value.String())
	target := p.credentialTarget(key)
	// 无法确认原值时拒绝持久写入，让插件只在内存使用本次临时输入。
	if _, _, err := systemCredentials.get(target); err != nil {
		return moejs.Undefined(), credentialError(err)
	}
	err = systemCredentials.set(target, value.String())
	return moejs.Undefined(), credentialError(err)
}

func (p *Plugin) credentialRemove(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
	key, err := credentialKey(args)
	if err != nil {
		return moejs.Undefined(), err
	}
	target := p.credentialTarget(key)
	// 与临时密钥的写保护一致：库不可读绝不靠删除重置，保留原值待恢复。
	if _, _, err := systemCredentials.get(target); err != nil {
		return moejs.Undefined(), credentialError(err)
	}
	err = systemCredentials.remove(target)
	return moejs.Undefined(), credentialError(err)
}

func credentialError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errCredentialsUnsupported) {
		return errCredentialsUnsupported
	}
	// 原生错误可能带目标、账户或密钥；不将其拼入脚本异常或宿主日志。
	return errCredentialsUnavailable
}
