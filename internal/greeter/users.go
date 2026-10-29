package greeter

import (
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/stubbedev/lys-shell/useraccount"
)

// uidMin and uidMax bound the regular-user range offered in the user
// list: below is system accounts, above reserved ranges and nobody.
const (
	uidMin = 1000
	uidMax = 59999
)

// User is one login account offered in the user list (users.rs).
type User struct {
	// Name is the login name greetd authenticates.
	Name string
	// UID keys the cursor record the account's last session left.
	UID uint32
	// DisplayName is the GECOS full name, else Name.
	DisplayName string
	// Home seeds ~/.face and the cursor read from dotfiles (it may be
	// unreadable).
	Home string
	// Icon is a readable avatar path, "" when none.
	Icon string
}

// ReadUsers reads the login users from /etc/passwd.
func ReadUsers() []User {
	data, _ := os.ReadFile("/etc/passwd")
	return parsePasswd(string(data))
}

// LoadUsers is ReadUsers with the avatars resolved.
func LoadUsers() []User {
	users := ReadUsers()
	for i := range users {
		users[i].Icon = useraccount.FindAvatar(users[i].Name, users[i].Home)
	}
	return users
}

// parsePasswd keeps regular-range accounts with a login shell, sorted
// by lowercased display name.
func parsePasswd(text string) []User {
	var users []User
	for line := range strings.SplitSeq(text, "\n") {
		f := strings.Split(line, ":")
		if len(f) != 7 {
			continue
		}
		uid, err := strconv.ParseUint(f[2], 10, 32)
		if err != nil || uid < uidMin || uid > uidMax || !isLoginShell(f[6]) {
			continue
		}
		full, _, _ := strings.Cut(f[4], ",")
		full = strings.TrimSpace(full)
		display := full
		if display == "" {
			display = f[0]
		}
		users = append(users, User{Name: f[0], UID: uint32(uid), DisplayName: display, Home: f[5]})
	}
	sort.SliceStable(users, func(i, j int) bool {
		return strings.ToLower(users[i].DisplayName) < strings.ToLower(users[j].DisplayName)
	})
	return users
}

// isLoginShell reports whether shell allows an interactive login.
func isLoginShell(shell string) bool {
	return shell != "" && !strings.HasSuffix(shell, "nologin") &&
		!strings.HasSuffix(shell, "/false") && shell != "/bin/sync"
}
