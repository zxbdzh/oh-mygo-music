package plugin

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/webapi"

	"mygo-music/internal/i18n"
	"mygo-music/internal/music"
)

// openURL opens a page in the listener's browser. The app sets it.
var openURL = func(string) error { return errors.New("can't open pages here") }

// SetOpenURL says how to open a page in the listener's browser.
func SetOpenURL(open func(string) error) { openURL = open }

// WebSignIn is a sign-in on a page the app shows, in a window of its own, as
// host.webSignIn asks for one: what a plugin has nothing else to sign in with,
// as a service's website is the only way in. The page is apart from every
// other, the listener's browser too, and forgotten after.
type WebSignIn struct {
	// Plugin and Name are the plugin's ID and its name, for the window.
	Plugin string `json:"-"`
	Name   string `json:"-"`
	// URL is the page to sign in on.
	URL string `json:"url"`
	// CookiesOf is where the cookies are for: those a request to this
	// address would send are the answer. The plugin's network permission
	// has to reach it.
	CookiesOf string `json:"cookiesOf"`
	// Until names the cookies that say the listener signed in: once all of
	// them are there, the window closes and the cookies go to the plugin.
	Until []string `json:"until"`
	// UserAgent is what the page is told it's shown in, for a site that
	// turns down what it doesn't know.
	UserAgent string `json:"userAgent,omitempty"`
}

// webSignIn shows a sign-in page and answers its cookies as a Cookie header.
// The app sets it.
var webSignIn = func(WebSignIn) (string, error) { return "", errors.New("can't show a sign-in page here") }

// SetWebSignIn says how to show a sign-in page, and get its cookies.
func SetWebSignIn(show func(WebSignIn) (string, error)) { webSignIn = show }

type native = func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error)

// installHost gives the runtime the host global and console.
func (p *Plugin) installHost(v *vm) error {
	rt := v.rt
	logFn := func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		parts := make([]string, len(args))
		for i, a := range args {
			parts[i] = a.String()
		}
		log.Printf("plugin %s: %s", p.manifest.ID, p.diagnostics.redact(strings.Join(parts, " ")))
		return moejs.Undefined(), nil
	}
	if err := rt.SetGlobal("console", map[string]any{
		"log":   moejs.NativeFunc(logFn),
		"info":  moejs.NativeFunc(logFn),
		"warn":  moejs.NativeFunc(logFn),
		"error": moejs.NativeFunc(logFn),
	}); err != nil {
		return err
	}
	return rt.SetGlobal("host", map[string]any{
		"platform": runtime.GOOS,
		"arch":     runtime.GOARCH,
		"dir":      p.dir,
		"dataDir":  p.data,
		"storage": map[string]any{
			"get":    moejs.NativeFunc(p.storageGet(v)),
			"keys":   moejs.NativeFunc(p.storageKeys(v)),
			"set":    moejs.NativeFunc(p.storageSet(v)),
			"remove": moejs.NativeFunc(p.storageRemove),
		},
		// 长期密钥只交给系统凭据库，不经过插件的普通 storage/settings。
		"credentials": map[string]any{
			"get":    moejs.NativeFunc(p.credentialGet),
			"set":    moejs.NativeFunc(p.credentialSet),
			"remove": moejs.NativeFunc(p.credentialRemove),
		},
		// language answers the listener's, as the app speaks it: en or zh-CN.
		// It changes as they change it, without the plugin starting over.
		"language": moejs.NativeFunc(func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
			return moejs.String(i18n.Language()), nil
		}),
		// t answers the text in the listener's language, from the plugin's
		// locales, or as it is when it has none for it.
		"t": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			return moejs.String(p.t(moejs.Arg(args, 0).String())), nil
		}),
		"setting": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			return moejs.String(p.Setting(moejs.Arg(args, 0).String())), nil
		}),
		"openURL": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			address := moejs.Arg(args, 0).String()
			// Only pages: a plugin can't have the system open just anything.
			if u, err := url.Parse(address); err != nil || (u.Scheme != "https" && u.Scheme != "http") {
				return moejs.Undefined(), fmt.Errorf("not a web address: %s", address)
			}
			return moejs.Undefined(), openURL(address)
		}),
		"signInPrompt": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			var prompt music.SignInPrompt
			if err := v.rt.ToGoInto(moejs.Arg(args, 0), &prompt); err != nil {
				return moejs.Undefined(), err
			}
			p.mu.Lock()
			show := p.prompt
			p.mu.Unlock()
			if show != nil {
				show(prompt)
			}
			return moejs.Undefined(), nil
		}),
		"crypto": map[string]any{
			// OAuth state 必须来自系统安全随机源，不能退回脚本 Math.random。
			"randomHex": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
				var value [32]byte
				if _, err := rand.Read(value[:]); err != nil {
					return moejs.Undefined(), errors.New("secure random source is unavailable")
				}
				return moejs.String(hex.EncodeToString(value[:])), nil
			}),
			"md5": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
				sum := md5.Sum([]byte(moejs.Arg(args, 0).String()))
				return moejs.String(hex.EncodeToString(sum[:])), nil
			}),
			"aesEcbEncrypt": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
				out, err := aesECB([]byte(moejs.Arg(args, 0).String()), []byte(moejs.Arg(args, 1).String()))
				if err != nil {
					return moejs.Undefined(), err
				}
				return moejs.String(hex.EncodeToString(out)), nil
			}),
		},
		"removeDataDir": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			name := moejs.Arg(args, 0).String()
			// Only something within its own data, and not all of it, which holds ours.
			if !filepath.IsLocal(name) || filepath.Clean(name) == "." {
				return moejs.Undefined(), fmt.Errorf("not a folder of the plugin's data: %s", name)
			}
			return moejs.Undefined(), os.RemoveAll(filepath.Join(p.data, name))
		}),
		"waitForRedirect": moejs.NativeFunc(p.waitForRedirect(v)),
		"webSignIn":       moejs.NativeFunc(p.webSignIn(v)),
		"spawn":           moejs.NativeFunc(p.spawn(v)),
	})
}

func (p *Plugin) storageGet(v *vm) native {
	return func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		p.mu.Lock()
		raw, ok := p.storage[moejs.Arg(args, 0).String()]
		p.mu.Unlock()
		if !ok {
			return moejs.Undefined(), nil
		}
		return v.rt.ParseJSON(raw)
	}
}

// 仅枚举当前插件普通存储中匹配前缀的键，不读取值或系统凭据。
func (p *Plugin) storageKeys(v *vm) native {
	return func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		arg := moejs.Arg(args, 0)
		if !arg.IsString() || arg.String() == "" {
			return moejs.Undefined(), errors.New("storage.keys needs a non-empty string prefix")
		}
		prefix := arg.String()
		keys := []string{}
		p.mu.Lock()
		for key := range p.storage {
			if strings.HasPrefix(key, prefix) {
				keys = append(keys, key)
			}
		}
		p.mu.Unlock()
		return v.rt.FromGo(keys)
	}
}

func (p *Plugin) storageSet(v *vm) native {
	return func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		raw, err := v.rt.AppendJSON(nil, moejs.Arg(args, 1))
		if err != nil {
			return moejs.Undefined(), err
		}
		p.mu.Lock()
		p.storage[moejs.Arg(args, 0).String()] = raw
		err = p.writeJSON("storage.json", p.storage)
		p.mu.Unlock()
		return moejs.Undefined(), err
	}
}

func (p *Plugin) storageRemove(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.storage, moejs.Arg(args, 0).String())
	return moejs.Undefined(), p.writeJSON("storage.json", p.storage)
}

// waitForRedirect is the end of a browser sign-in: it listens on this machine
// alone for the redirect back, and answers what it came with.
func (p *Plugin) waitForRedirect(v *vm) native {
	return func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		if len(p.manifest.Permissions.Network) == 0 {
			return moejs.Undefined(), errors.New("waitForRedirect needs the network permission")
		}
		var o struct {
			Port    int    `json:"port"`
			Path    string `json:"path"`
			Timeout int64  `json:"timeout"` // milliseconds
			Page    string `json:"page"`    // what the browser shows after
		}
		if err := v.rt.ToGoInto(moejs.Arg(args, 0), &o); err != nil {
			return moejs.Undefined(), err
		}
		if o.Page == "" {
			o.Page = "You're signed in. You can close this page."
		}
		wait := time.Duration(o.Timeout) * time.Millisecond
		if wait <= 0 {
			wait = signInTimeout
		}
		promise, settle := v.loop.NewPromise()
		go func() {
			query, err := awaitRedirect(o.Port, o.Path, o.Page, wait)
			settle(func(rt *moejs.Runtime) (moejs.Value, error) {
				if err != nil {
					return moejs.Undefined(), err
				}
				return rt.FromGo(query)
			})
		}()
		return promise, nil
	}
}

func awaitRedirect(port int, path, page string, wait time.Duration) (map[string]any, error) {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	got := make(chan map[string]any, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if path != "" && r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		query := map[string]any{}
		for k, vs := range r.URL.Query() {
			query[k] = vs[0]
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, page)
		select {
		case got <- query:
		default:
		}
	})}
	go srv.Serve(l)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()
	select {
	case q := <-got:
		return q, nil
	case <-time.After(wait):
		return nil, errors.New("timed out waiting for the sign-in to come back")
	}
}

// webSignIn has the app show a sign-in page, and answers {cookie}: the Cookie
// header of the address asked for, once the cookies named are all there. Only
// a plugin's own addresses: those its network permission reaches.
func (p *Plugin) webSignIn(v *vm) native {
	return func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		var o WebSignIn
		if err := v.rt.ToGoInto(moejs.Arg(args, 0), &o); err != nil {
			return moejs.Undefined(), err
		}
		if err := p.checkWebSignIn(o); err != nil {
			return moejs.Undefined(), err
		}
		o.Plugin, o.Name = p.manifest.ID, p.manifest.Name
		promise, settle := v.loop.NewPromise()
		go func() {
			cookie, err := webSignIn(o)
			settle(func(rt *moejs.Runtime) (moejs.Value, error) {
				if err != nil {
					return moejs.Undefined(), err
				}
				// 登录结果在持久保存前也可能被脚本记录，须先纳入诊断保护。
				p.diagnostics.remember(cookie)
				return rt.FromGo(map[string]any{"cookie": cookie})
			})
		}()
		return promise, nil
	}
}

func (p *Plugin) checkWebSignIn(o WebSignIn) error {
	network := p.manifest.Permissions.Network
	if len(network) == 0 {
		return errors.New("webSignIn needs the network permission")
	}
	if u, err := url.Parse(o.URL); err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("not a secure web address to sign in on: %s", o.URL)
	}
	u, err := url.Parse(o.CookiesOf)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("cookiesOf isn't a secure web address: %s", o.CookiesOf)
	}
	if err := webapi.AllowHosts(network...)(&http.Request{URL: u}); err != nil {
		return fmt.Errorf("the plugin can't reach %s, so it can't have its cookies", u.Hostname())
	}
	if len(o.Until) == 0 {
		return errors.New("webSignIn needs the names of the cookies that say it's done, as until")
	}
	return nil
}

// program is what a plugin started.
type program struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan string // what it wrote, closed when it stops
	done  chan error
}

func (v *vm) killPrograms() {
	v.mu.Lock()
	programs := v.programs
	v.programs = map[*program]bool{}
	v.mu.Unlock()
	for pr := range programs {
		pr.cmd.Process.Kill()
	}
}

// spawn starts a program, by a path from the plugin's directory or absolute,
// or a name on PATH, for a plugin whose process permission names it, once the
// listener granted it. It ends
// with the plugin, at the latest.
func (p *Plugin) spawn(v *vm) native {
	return func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		programs := p.manifest.Permissions.Process
		if len(programs) == 0 {
			return moejs.Undefined(), errors.New("spawn needs the process permission")
		}
		if !p.Granted() {
			return moejs.Undefined(), errors.New("the listener hasn't allowed this plugin to start programs")
		}
		rt := v.rt
		command := moejs.Arg(args, 0).String()
		var cmdArgs []string
		if len(args) > 1 {
			if err := rt.ToGoInto(args[1], &cmdArgs); err != nil {
				return moejs.Undefined(), err
			}
		}
		if !filepath.IsAbs(command) && strings.ContainsAny(command, `/\`) {
			command = filepath.Join(p.dir, command)
		}
		if !programs.Allows(p.dir, command) {
			return moejs.Undefined(), fmt.Errorf("%s isn't one of the programs in the plugin's process permission", moejs.Arg(args, 0).String())
		}
		cmd := exec.Command(command, cmdArgs...)
		cmd.Dir = p.dir
		hideConsole(cmd)
		diagnostics := &logWriter{plugin: p}
		stderr := &tailWriter{to: diagnostics}
		cmd.Stderr = stderr
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return moejs.Undefined(), err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return moejs.Undefined(), err
		}
		if err := cmd.Start(); err != nil {
			return moejs.Undefined(), err
		}
		pr := &program{cmd: cmd, stdin: stdin, lines: make(chan string, 256), done: make(chan error, 1)}
		v.mu.Lock()
		closed := v.closed
		if !closed {
			v.programs[pr] = true
		}
		v.mu.Unlock()
		if closed {
			cmd.Process.Kill()
		}
		go func() {
			sc := bufio.NewScanner(stdout)
			sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
			for sc.Scan() {
				pr.lines <- sc.Text()
			}
			close(pr.lines)
			err := cmd.Wait()
			// Wait 已等待 stderr 复制结束，此时才能安全输出未换行的最后一段。
			diagnostics.flush()
			v.mu.Lock()
			delete(v.programs, pr)
			v.mu.Unlock()
			pr.done <- err
			close(pr.done)
		}()
		return rt.FromGo(map[string]any{
			"pid": cmd.Process.Pid,
			// errorOutput answers the end of what it wrote to stderr, to say why it stopped.
			"errorOutput": moejs.NativeFunc(func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
				return moejs.String(stderr.String()), nil
			}),
			"write": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
				_, err := io.WriteString(stdin, moejs.Arg(a, 0).String())
				return moejs.Undefined(), err
			}),
			"closeStdin": moejs.NativeFunc(func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
				return moejs.Undefined(), stdin.Close()
			}),
			"kill": moejs.NativeFunc(func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
				return moejs.Undefined(), cmd.Process.Kill()
			}),
			// readLine answers its next line of output, or null once it's done.
			"readLine": moejs.NativeFunc(func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
				promise, settle := v.loop.NewPromise()
				go func() {
					line, ok := <-pr.lines
					settle(func(rt *moejs.Runtime) (moejs.Value, error) {
						if !ok {
							return moejs.Null(), nil
						}
						return moejs.String(line), nil
					})
				}()
				return promise, nil
			}),
			// wait answers its exit code once it stopped.
			"wait": moejs.NativeFunc(func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
				promise, settle := v.loop.NewPromise()
				go func() {
					err := <-pr.done
					settle(func(rt *moejs.Runtime) (moejs.Value, error) {
						code := 0
						var exit *exec.ExitError
						if errors.As(err, &exit) {
							code = exit.ExitCode()
						}
						return rt.FromGo(code)
					})
				}()
				return promise, nil
			}),
		})
	}
}

// aesECB encrypts text as AES in ECB mode with PKCS#7 padding. Plugins have it for
// the services whose requests are made that way: it's not for keeping secrets.
func aesECB(key, text []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	pad := aes.BlockSize - len(text)%aes.BlockSize
	text = append(append([]byte(nil), text...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(text))
	for i := 0; i < len(text); i += aes.BlockSize {
		block.Encrypt(out[i:i+aes.BlockSize], text[i:i+aes.BlockSize])
	}
	return out, nil
}

// tailWriter passes on what's written and keeps the end of it.
type tailWriter struct {
	to   io.Writer
	mu   sync.Mutex
	tail []byte
}

const tailSize = 8 << 10

func (w *tailWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	w.tail = append(w.tail, b...)
	if len(w.tail) > tailSize {
		w.tail = append(w.tail[:0], w.tail[len(w.tail)-tailSize:]...)
	}
	w.mu.Unlock()
	return w.to.Write(b)
}

func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.tail)
}

// logWriter 只处理诊断副本；tailWriter 保留原始 stderr 供脚本通信。
// exec.Cmd 串行写入此 writer，Wait 返回后才刷新末行，不与 Write 并发。
type logWriter struct {
	plugin     *Plugin
	line       []byte
	discarding bool
}

const diagnosticLineLimit = 8 << 10

func (w *logWriter) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		end := bytes.IndexByte(b, '\n')
		part := b
		if end >= 0 {
			part = b[:end]
		}
		if !w.discarding {
			if len(part) > diagnosticLineLimit-len(w.line) {
				// 不能只输出截断前缀：凭据或签名地址可能横跨任意管道分块。
				w.line = nil
				w.discarding = true
			} else {
				w.line = append(w.line, part...)
			}
		}
		if end < 0 {
			break
		}
		w.flush()
		b = b[end+1:]
	}
	return n, nil
}

func (w *logWriter) flush() {
	if w.discarding {
		log.Printf("plugin %s: [REDACTED LONG LINE]", w.plugin.manifest.ID)
	} else if len(w.line) > 0 {
		log.Printf("plugin %s: %s", w.plugin.manifest.ID, w.plugin.diagnostics.redact(string(w.line)))
	}
	w.line = w.line[:0]
	w.discarding = false
}
