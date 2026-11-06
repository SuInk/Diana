// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package browseruser 给以 root 运行的 Diana 提供一个普通用户，用来拉起 Chromium。
//
// Chromium 不允许 root 带进程沙箱启动（「Running as root without --no-sandbox is not
// supported」），而网页渲染打开的是群成员发来的链接，沙箱是挡渲染器漏洞利用的那一层。
// 关沙箱不是好办法，Chromium 官方的说法也是「别用 root 跑」：所以 Diana 自己是 root
// 时，浏览器换成 diana 用户启动，沙箱照开，主人什么都不用配。
//
// 只在 Linux 上做：Termux 没有 root 也没有沙箱，macOS/Windows 没有以 root 跑服务的
// 部署方式。容器镜像的入口脚本本来就降到 uid 10001 的 diana，用不到这里。
package browseruser

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/internal/termux"
)

const (
	// Name 是降权用的账号名，和容器镜像里的运行用户同名。
	Name = "diana"
	// Home 是这个账号能读写的固定目录：root 装的数据目录常在 /root 下，diana 进不去，
	// 内置浏览器的 profile 和下载的 Chrome 就放这里。
	Home = "/var/lib/diana-browser"
)

// Account 是拉起 Chromium 用的账号。
type Account struct {
	UID  int
	GID  int
	Home string
}

// retryAfter 是建账号失败后多久再试：失败多半是缺 useradd 或者只读根文件系统，
// 每次启动浏览器都去试一遍没有意义。
const retryAfter = 5 * time.Minute

var cache struct {
	sync.Mutex
	account  Account
	ok       bool
	failedAt time.Time
	reason   string
}

// lookupOverride 给测试替换账号查找。测试里不设它就当没有账号：CI 容器以 root 跑
// 测试，不能真去建用户。
var lookupOverride func() (Account, bool, string)

// SetLookupForTest 在测试里固定 Lookup 的结果，返回恢复函数。
func SetLookupForTest(account Account, ok bool, reason string) func() {
	previous := lookupOverride
	lookupOverride = func() (Account, bool, string) { return account, ok, reason }
	return func() { lookupOverride = previous }
}

// Lookup 返回拉起 Chromium 该用的账号：当前不是 root，或者不在 Linux 上，返回 false，
// 照当前身份启动。账号不存在时用 useradd/adduser 建一个没有登录 shell 的系统账号；
// 建不出来返回 false，原因见 Reason。调用方自己判断 Termux。
func Lookup() (Account, bool) {
	account, ok, _ := lookup()
	return account, ok
}

// Reason 说明 Lookup 为什么没给出账号；给出了或者不需要时为空。
func Reason() string {
	_, _, reason := lookup()
	return reason
}

func lookup() (Account, bool, string) {
	if lookupOverride != nil {
		return lookupOverride()
	}
	if testing.Testing() || runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return Account{}, false, ""
	}
	cache.Lock()
	defer cache.Unlock()
	if cache.ok {
		return cache.account, true, ""
	}
	if !cache.failedAt.IsZero() && time.Since(cache.failedAt) < retryAfter {
		return Account{}, false, cache.reason
	}
	account, err := ensureAccount()
	if err != nil {
		cache.failedAt, cache.reason = time.Now(), err.Error()
		log.Printf("WARN 以 root 运行，但没能准备 %s 用户来降权启动浏览器：%v", Name, err)
		return Account{}, false, cache.reason
	}
	cache.account, cache.ok, cache.failedAt, cache.reason = account, true, time.Time{}, ""
	return account, true, ""
}

func ensureAccount() (Account, error) {
	found, err := user.Lookup(Name)
	var unknown user.UnknownUserError
	if errors.As(err, &unknown) {
		if createErr := createAccount(); createErr != nil {
			return Account{}, createErr
		}
		log.Printf("INFO 以 root 运行，已建系统用户 %s 用来降权启动浏览器（进程沙箱照开）", Name)
		found, err = user.Lookup(Name)
	}
	if err != nil {
		return Account{}, fmt.Errorf("查 %s 用户失败：%w", Name, err)
	}
	uid, uidErr := strconv.Atoi(found.Uid)
	gid, gidErr := strconv.Atoi(found.Gid)
	if uidErr != nil || gidErr != nil {
		return Account{}, fmt.Errorf("%s 用户的 uid/gid 不是数字：%s/%s", Name, found.Uid, found.Gid)
	}
	if uid == 0 {
		return Account{}, fmt.Errorf("%s 用户的 uid 是 0，降不了权", Name)
	}
	account := Account{UID: uid, GID: gid, Home: Home}
	if err := os.MkdirAll(Home, 0o755); err != nil {
		return Account{}, fmt.Errorf("建 %s 失败：%w", Home, err)
	}
	if err := os.Chown(Home, uid, gid); err != nil {
		return Account{}, fmt.Errorf("把 %s 交给 %s 用户失败：%w", Home, Name, err)
	}
	// 里面的内置浏览器 profile 各自是 0700；这一层只要 diana 能进、root 下载的
	// Chrome 能被执行。
	if err := os.Chmod(Home, 0o755); err != nil {
		return Account{}, err
	}
	return account, nil
}

// createAccount 建一个系统账号：不能登录、家目录指向 Home。Debian/Ubuntu/Fedora 有
// useradd，Alpine 这种 busybox 系统只有 adduser。
func createAccount() error {
	nologin := "/usr/sbin/nologin"
	if _, err := os.Stat(nologin); err != nil {
		nologin = "/sbin/nologin"
	}
	if path, err := exec.LookPath("useradd"); err == nil {
		out, err := exec.Command(path, "--system", "--user-group", "--no-create-home", "--home-dir", Home, "--shell", nologin, Name).CombinedOutput()
		if err != nil {
			return fmt.Errorf("useradd 失败：%v：%s", err, out)
		}
		return nil
	}
	if path, err := exec.LookPath("adduser"); err == nil {
		out, err := exec.Command(path, "-S", "-D", "-H", "-h", Home, "-s", nologin, Name).CombinedOutput()
		if err != nil {
			return fmt.Errorf("adduser 失败：%v：%s", err, out)
		}
		return nil
	}
	return errors.New("系统里没有 useradd/adduser，建不了用户")
}

// Own 把这些目录（连同里面的东西）交给账号。浏览器要写自己的 profile、缓存和临时
// 文件；已经归它的跳过，常驻浏览器的 profile 每次启动只改新出现的那些。
func (a Account) Own(paths ...string) error {
	for _, root := range paths {
		if root == "" {
			continue
		}
		err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if owned(path, a.UID, a.GID) {
				return nil
			}
			return os.Lchown(path, a.UID, a.GID)
		})
		if err != nil {
			return fmt.Errorf("把浏览器目录交给 %s 用户失败：%w", Name, err)
		}
	}
	return nil
}

// CanReach 判断账号能不能走到 path：沿途每一层目录都要有对它的执行权限，path 本身
// 要能读。可执行文件还要能执行。只按主组算——降权时不带附加组。
func (a Account) CanReach(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	info, err := os.Stat(abs)
	if err != nil {
		return false
	}
	need := fs.FileMode(0o4)
	if info.IsDir() || info.Mode()&0o111 != 0 {
		need |= 0o1
	}
	if !a.allowed(info, need) {
		return false
	}
	return a.CanTraverse(filepath.Dir(abs))
}

// CanTraverse 判断账号能不能穿过 dir 及其上面每一层目录。
func (a Account) CanTraverse(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	for {
		info, err := os.Stat(abs)
		if err != nil || !a.allowed(info, 0o1) {
			return false
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return true
		}
		abs = parent
	}
}

// DownloadDir 是 root 运行时依赖页下载 Chrome 的落点：diana 用户够得着的 Home/browser。
// 不需要降权或降不了时返回空串，照原来的数据目录。
func DownloadDir() string {
	if termux.Detected() {
		return ""
	}
	if _, ok := Lookup(); !ok {
		return ""
	}
	return filepath.Join(Home, "browser")
}
