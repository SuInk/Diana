package assistant

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

var repositoryWatchLoginPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,98}[a-z0-9])?(?:\[bot\])?$`)

func normalizeRepositoryWatchBlockedUsers(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	for _, value := range values {
		login := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "@"))
		if !repositoryWatchLoginPattern.MatchString(login) {
			return nil, fmt.Errorf("通知屏蔽名单请填写 GitHub 用户名，不要填写链接或显示名称：%q", value)
		}
		if !slices.Contains(result, login) {
			result = append(result, login)
		}
	}
	if len(result) > 100 {
		return nil, fmt.Errorf("每条仓库订阅最多屏蔽 100 个用户")
	}
	slices.Sort(result)
	return result, nil
}

func repositoryWatchUserBlocked(users []string, login string) bool {
	login = strings.TrimSpace(login)
	if login == "" {
		return false
	}
	for _, user := range users {
		if strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(user), "@"), login) {
			return true
		}
	}
	return false
}

// GitHubUserIdentity only contains public profile fields used by the optional UI hint.
type GitHubUserIdentity struct {
	Login string `json:"login"`
	Name  string `json:"name,omitempty"`
}

func (p *RepositoryWatchPlugin) LookupUser(ctx context.Context, login string, settings SettingValues) *GitHubUserIdentity {
	users, err := normalizeRepositoryWatchBlockedUsers([]string{login})
	if err != nil {
		return nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, credentialCheckTimeout)
	defer cancel()
	var user GitHubUserIdentity
	if err := p.getJSON(lookupCtx, "/users/"+url.PathEscape(users[0]), settings, &user); err != nil || user.Login == "" {
		return nil
	}
	return &user
}
