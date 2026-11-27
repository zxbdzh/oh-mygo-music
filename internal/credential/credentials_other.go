//go:build !windows

package credential

type platform struct{}

// 首版只交付 Windows；其他系统明确失败，不静默将秘密降级到明文文件。
func (platform) Get(string) (string, bool, error) { return "", false, ErrUnsupported }
func (platform) Set(string, string) error         { return ErrUnsupported }
func (platform) Remove(string) error              { return ErrUnsupported }
