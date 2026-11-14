// Package github is a minimal GitHub REST client: just enough to open a
// pull request that changes one file, forking when the user can't push.
package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Repo identifies a repository.
type Repo struct{ Owner, Name string }

func (r Repo) String() string { return r.Owner + "/" + r.Name }

var (
	httpsRe = regexp.MustCompile(`^https?://(?:www\.)?github\.com/([\w.-]+)/([\w.-]+?)(?:\.git)?/?$`)
	sshRe   = regexp.MustCompile(`^(?:ssh://)?git@github\.com[:/]([\w.-]+)/([\w.-]+?)(?:\.git)?$`)
)

// ParseRepo extracts owner/name from a GitHub clone URL. ok is false for
// anything that isn't a github.com repository URL.
func ParseRepo(u string) (Repo, bool) {
	u = strings.TrimSpace(u)
	for _, re := range []*regexp.Regexp{httpsRe, sshRe} {
		if m := re.FindStringSubmatch(u); m != nil {
			return Repo{m[1], m[2]}, true
		}
	}
	return Repo{}, false
}

// Client talks to the GitHub API.
type Client struct {
	Token   string
	BaseURL string // default https://api.github.com
	HTTP    *http.Client
}

// APIError is a non-2xx response.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("github: %d %s", e.Status, e.Message) }

// IsStatus reports whether err is an APIError with the given status.
func IsStatus(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	base := c.BaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "vanguard.run")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
			Errors  []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Message
		for _, x := range e.Errors {
			if x.Message != "" {
				msg += ": " + x.Message
			}
		}
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return &APIError{Status: res.StatusCode, Message: msg}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func escapePath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// User returns the authenticated user's login.
func (c *Client) User(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	err := c.do(ctx, http.MethodGet, "/user", nil, &u)
	return u.Login, err
}

// RepoInfo is what we need to know about a repository.
type RepoInfo struct {
	DefaultBranch string `json:"default_branch"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
	Name        string `json:"name"`
	Permissions struct {
		Push bool `json:"push"`
	} `json:"permissions"`
}

// Repo fetches repository metadata.
func (c *Client) Repo(ctx context.Context, r Repo) (*RepoInfo, error) {
	var info RepoInfo
	err := c.do(ctx, http.MethodGet, "/repos/"+r.String(), nil, &info)
	return &info, err
}

// Fork forks r into the authenticated user's account (or returns the
// existing fork) and waits until it is usable.
func (c *Client) Fork(ctx context.Context, r Repo) (Repo, error) {
	var info RepoInfo
	if err := c.do(ctx, http.MethodPost, "/repos/"+r.String()+"/forks", map[string]any{}, &info); err != nil {
		return Repo{}, err
	}
	fork := Repo{info.Owner.Login, info.Name}
	for i := 0; i < 30; i++ { // forks are created asynchronously
		if _, err := c.Repo(ctx, fork); err == nil {
			return fork, nil
		}
		select {
		case <-ctx.Done():
			return Repo{}, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return Repo{}, fmt.Errorf("github: fork %s was not ready in time", fork)
}

// SyncFork fast-forwards a fork's branch to its upstream (merge-upstream).
func (c *Client) SyncFork(ctx context.Context, fork Repo, branch string) error {
	return c.do(ctx, http.MethodPost, "/repos/"+fork.String()+"/merge-upstream", map[string]string{"branch": branch}, nil)
}

// BranchSHA returns the commit a branch points at.
func (c *Client) BranchSHA(ctx context.Context, r Repo, branch string) (string, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	err := c.do(ctx, http.MethodGet, "/repos/"+r.String()+"/git/ref/heads/"+escapePath(branch), nil, &ref)
	return ref.Object.SHA, err
}

// CreateBranch creates branch at sha.
func (c *Client) CreateBranch(ctx context.Context, r Repo, branch, sha string) error {
	return c.do(ctx, http.MethodPost, "/repos/"+r.String()+"/git/refs",
		map[string]string{"ref": "refs/heads/" + branch, "sha": sha}, nil)
}

// File returns a file's contents and blob SHA at ref.
func (c *Client) File(ctx context.Context, r Repo, path, ref string) ([]byte, string, error) {
	var f struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		SHA      string `json:"sha"`
		Type     string `json:"type"`
	}
	p := "/repos/" + r.String() + "/contents/" + escapePath(path)
	if ref != "" {
		p += "?ref=" + url.QueryEscape(ref)
	}
	if err := c.do(ctx, http.MethodGet, p, nil, &f); err != nil {
		return nil, "", err
	}
	if f.Type != "file" || f.Encoding != "base64" {
		return nil, "", fmt.Errorf("github: %s is not a regular file", path)
	}
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(f.Content, "\n", ""))
	return b, f.SHA, err
}

// Identity is a commit author/committer.
type Identity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// UpdateFile commits new contents for an existing file on branch. author,
// if set, is used as the commit's author and committer (e.g. to match a DCO
// Signed-off-by line); otherwise GitHub uses the token's user.
func (c *Client) UpdateFile(ctx context.Context, r Repo, path, branch, blobSHA, message string, content []byte, author *Identity) error {
	body := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(content),
		"sha":     blobSHA,
		"branch":  branch,
	}
	if author != nil {
		body["author"], body["committer"] = author, author
	}
	return c.do(ctx, http.MethodPut, "/repos/"+r.String()+"/contents/"+escapePath(path), body, nil)
}

// Entry is one item in a directory listing.
type Entry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"` // "file" or "dir"
}

// ListDir lists a directory at ref ("" path = repository root). A missing
// directory yields no entries.
func (c *Client) ListDir(ctx context.Context, r Repo, path, ref string) ([]Entry, error) {
	p := "/repos/" + r.String() + "/contents"
	if path = strings.Trim(path, "/"); path != "" {
		p += "/" + escapePath(path)
	}
	if ref != "" {
		p += "?ref=" + url.QueryEscape(ref)
	}
	var entries []Entry
	if err := c.do(ctx, http.MethodGet, p, nil, &entries); err != nil {
		if IsStatus(err, http.StatusNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return entries, nil
}

// PullRequest is a created PR.
type PullRequest struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
}

// CreatePR opens a pull request. head is "branch" for the same repo or
// "owner:branch" from a fork.
func (c *Client) CreatePR(ctx context.Context, r Repo, title, body, head, base string) (*PullRequest, error) {
	var pr PullRequest
	err := c.do(ctx, http.MethodPost, "/repos/"+r.String()+"/pulls", map[string]any{
		"title": title, "body": body, "head": head, "base": base, "maintainer_can_modify": true,
	}, &pr)
	return &pr, err
}
