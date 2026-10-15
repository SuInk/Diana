/** Accept usernames, mentions, and GitHub profile links without interpreting display names. */
export function githubUserLogins(value: string): string[] {
  const users: string[] = [];
  for (const part of value.split(/[\s,，;；、]+/).filter(Boolean)) {
    let login = part.replace(/^@/, "");
    if (/^(?:https?:\/\/)?(?:www\.)?github\.com\//i.test(login)) {
      try {
        const url = new URL(/^https?:\/\//i.test(login) ? login : `https://${login}`);
        login = decodeURIComponent(url.pathname.replace(/^\//, "").replace(/\/$/, ""));
      } catch { /* Leave invalid input for the server to reject. */ }
    }
    login = login.toLowerCase();
    if (!users.includes(login)) users.push(login);
  }
  return users;
}

export function isGitHubLogin(login: string): boolean {
  return /^[a-z0-9](?:[a-z0-9-]{0,98}[a-z0-9])?(?:\[bot\])?$/.test(login);
}
