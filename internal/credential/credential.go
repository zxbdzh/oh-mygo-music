// Package credential stores small secrets in the operating system's credential store.
package credential

import "errors"

// BlobLimit is the largest value Windows GENERIC credentials accept.
const BlobLimit = 5 * 512

var (
	ErrUnavailable = errors.New("system credential store unavailable")
	ErrUnsupported = errors.New("system credentials unsupported on this platform")
)

// Store is the narrow surface both plugins and the app use.
type Store interface {
	Get(target string) (string, bool, error)
	Set(target, value string) error
	Remove(target string) error
}

// System is replaced only at the system boundary in tests.
var System Store = platform{}

func Get(target string) (string, bool, error) { return System.Get(target) }
func Set(target, value string) error          { return System.Set(target, value) }
func Remove(target string) error              { return System.Remove(target) }
