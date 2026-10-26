//go:build windows

package softdist

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
	"golang.org/x/sys/windows"
)

var logonUserProc = windows.NewLazySystemDLL("advapi32.dll").NewProc("LogonUserW")
var userenvDLL = windows.NewLazySystemDLL("userenv.dll")
var loadProfileProc = userenvDLL.NewProc("LoadUserProfileW")
var unloadProfileProc = userenvDLL.NewProc("UnloadUserProfile")

type profileInfo struct {
	Size, Flags                                                uint32
	UserName, ProfilePath, DefaultPath, ServerName, PolicyPath *uint16
	Profile                                                    windows.Handle
}

// Session owns exactly one execution token/profile. No target-user
// impersonation is used; the token is applied only to the installer child.
type Session struct {
	token                 windows.Token
	sid, scope, directory string
	environment           []string
	profile               windows.Handle
}

func OpenSession(e Execution) (s *Session, err error) {
	if err = e.Validate(); err != nil {
		return nil, err
	}
	mode := strings.ToLower(strings.TrimSpace(e.Mode))
	if mode == "" {
		mode = "system"
	}
	s = &Session{scope: strings.ToLower(strings.TrimSpace(e.DetectionScope))}
	if s.scope == "" {
		s.scope = "machine"
	}
	if strings.TrimSpace(e.DetectionScope) == "" && mode == "logged_in_user" {
		s.scope = "user"
	}
	if mode == "system" {
		tokenErr := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &s.token)
		if tokenErr != nil {
			return nil, fmt.Errorf("query agent execution identity: %w", tokenErr)
		}
		u, tokenErr := s.token.GetTokenUser()
		s.token.Close()
		s.token = 0
		if tokenErr != nil || u.User.Sid.String() != "S-1-5-18" {
			return nil, fmt.Errorf("SYSTEM execution requires the LocalSystem agent service")
		}
		return s, nil
	}
	owned := s
	defer func() {
		if err != nil {
			owned.Close()
		}
	}()
	if mode == "logged_in_user" {
		sessions, listErr := activeSessions()
		if listErr != nil {
			return nil, listErr
		}
		id, selectErr := chooseSession(sessions, e.SessionID)
		if selectErr != nil {
			return nil, selectErr
		}
		if err = windows.WTSQueryUserToken(id, &s.token); err != nil {
			return nil, fmt.Errorf("query user session token (requires LocalSystem/SeTcbPrivilege): %w", err)
		}
	} else {
		s.token, err = logonAccount(e)
		if err != nil {
			return nil, err
		}
	}
	u, err := s.token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("query execution user SID: %w", err)
	}
	s.sid = u.User.Sid.String()
	if s.sid == "S-1-5-18" {
		return nil, fmt.Errorf("user execution resolved to SYSTEM; refusing scope ambiguity")
	}
	account, _, _, err := u.User.Sid.LookupAccount("")
	if err != nil {
		return nil, fmt.Errorf("resolve profile account: %w", err)
	}
	name, err := windows.UTF16PtrFromString(account)
	if err != nil {
		return nil, fmt.Errorf("invalid profile account")
	}
	info := profileInfo{Flags: 1, UserName: name}
	info.Size = uint32(unsafe.Sizeof(info))
	err = withProfilePrivileges(func() error {
		ok, _, callErr := loadProfileProc.Call(uintptr(s.token), uintptr(unsafe.Pointer(&info)))
		if ok == 0 {
			return fmt.Errorf("load user profile (LocalSystem profile privileges required): %w", callErr)
		}
		s.profile = info.Profile
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.environment, err = s.token.Environ(false)
	if err != nil {
		return nil, fmt.Errorf("create user environment: %w", err)
	}
	for _, entry := range s.environment {
		key, value, found := strings.Cut(entry, "=")
		if found && strings.EqualFold(key, "USERPROFILE") {
			s.directory = value
			break
		}
	}
	if s.directory == "" {
		return nil, fmt.Errorf("loaded user profile has no USERPROFILE directory")
	}
	return s, nil
}

func activeSessions() ([]uint32, error) {
	var base *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &base, &count); err != nil {
		return nil, fmt.Errorf("enumerate active sessions: %w", err)
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(base)))
	var out []uint32
	for _, session := range unsafe.Slice(base, int(count)) {
		if session.State == 0 && session.SessionID != 0 {
			out = append(out, session.SessionID)
		}
	}
	return out, nil
}

func chooseSession(active []uint32, requested *uint32) (uint32, error) {
	if requested != nil {
		for _, id := range active {
			if id == *requested {
				return id, nil
			}
		}
		return 0, fmt.Errorf("requested user session is not active")
	}
	if len(active) == 0 {
		return 0, fmt.Errorf("no active logged-in user session")
	}
	if len(active) != 1 {
		return 0, fmt.Errorf("multiple active user sessions; specify session_id")
	}
	return active[0], nil
}

func logonAccount(e Execution) (windows.Token, error) {
	name, domain := strings.TrimSpace(e.Username), strings.TrimSpace(e.Domain)
	if before, after, found := strings.Cut(name, `\`); found {
		if domain != "" {
			return 0, fmt.Errorf("specify domain either separately or in username, not both")
		}
		domain, name = before, after
	}
	if strings.Contains(name, "@") && domain != "" {
		return 0, fmt.Errorf("UPN account must not have a separate domain")
	}
	if domain == "" && !strings.Contains(name, "@") {
		domain = "."
	}
	user, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, fmt.Errorf("invalid account username")
	}
	var domainPtr *uint16
	if domain != "" {
		domainPtr, err = windows.UTF16PtrFromString(domain)
		if err != nil {
			return 0, fmt.Errorf("invalid account domain")
		}
	}
	if !utf8.Valid(e.Password) {
		return 0, fmt.Errorf("account password must be UTF-8")
	}
	var runes []rune
	for rest := e.Password; len(rest) > 0; {
		r, n := utf8.DecodeRune(rest)
		if r == 0 {
			clear(runes)
			return 0, fmt.Errorf("account password contains NUL")
		}
		runes = append(runes, r)
		rest = rest[n:]
	}
	password := append(utf16.Encode(runes), 0)
	clear(runes)
	defer clear(password)
	var token windows.Token
	ok, _, callErr := logonUserProc.Call(uintptr(unsafe.Pointer(user)), uintptr(unsafe.Pointer(domainPtr)), uintptr(unsafe.Pointer(&password[0])), 4, 0, uintptr(unsafe.Pointer(&token)))
	if ok == 0 {
		return 0, fmt.Errorf("account batch logon failed (check credentials and batch-logon rights): %w", callErr)
	}
	return token, nil
}

func (s *Session) DetectionSID() string {
	if s != nil && s.scope == "user" {
		return s.sid
	}
	return ""
}
func (s *Session) Prepare(cmd *exec.Cmd) error {
	if s == nil || s.token == 0 {
		return nil
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Token = syscall.Token(s.token)
	cmd.SysProcAttr.CreationFlags |= 0x08000000
	cmd.Env = append([]string{}, s.environment...)
	cmd.Dir = s.directory
	return nil
}

func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.profile != 0 {
		err = withProfilePrivileges(func() error {
			ok, _, callErr := unloadProfileProc.Call(uintptr(s.token), uintptr(s.profile))
			if ok == 0 {
				return fmt.Errorf("unload user profile: %w", callErr)
			}
			return nil
		})
		s.profile = 0
	}
	if s.token != 0 {
		s.token.Close()
		s.token = 0
	}
	clear(s.environment)
	s.environment = nil
	return err
}

// Stage a delivered file with an initial DACL granting SYSTEM full access and
// only read/execute to the target SID. Never grant that user access to agent keys.
func (s *Session) StagePayload(path, root string) (string, func(), error) {
	if s == nil || s.token == 0 {
		return path, func() {}, nil
	}
	if root == "" {
		root = os.TempDir()
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", nil, err
	}
	dir := filepath.Join(root, "softdist-user-"+hex.EncodeToString(random[:]))
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FRFX;;;" + s.sid + ")")
	if err != nil {
		return "", nil, err
	}
	attrs := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	dirPtr, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return "", nil, err
	}
	if err = windows.CreateDirectory(dirPtr, attrs); err != nil {
		return "", nil, fmt.Errorf("create private user payload directory: %w", err)
	}
	dest := filepath.Join(dir, "payload"+filepath.Ext(path))
	cleanup := func() { _ = secure.SecureDelete(dest); _ = os.Remove(dir) }
	in, err := os.Open(path)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("stage user payload failed")
	}
	return dest, cleanup, nil
}
