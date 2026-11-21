// Package githubauth is the small part of GitHub's OAuth that admin sign-in
// needs (ADR 0016): build the authorization URL, exchange the code for an access
// token, and read the signed-in person's numeric id, login and name.
//
// Nothing here stores or logs anything. The access token is returned to the
// caller for one profile request and dropped. Errors say what failed and never
// repeat what GitHub or the browser sent, so a code, a token or an upstream
// message cannot reach a log or an error page through this package.
package githubauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Limits on what GitHub (or whoever answers at the configured address) can make
// this process read.
const (
	maxBody        = 256 << 10
	maxNameRunes   = 100
	defaultTimeout = 15 * time.Second
)

// ErrRejected means GitHub understood the request and refused the code: it is
// wrong, used or expired. Everything else is a failure to reach or understand
// GitHub.
var ErrRejected = errors.New("githubauth: GitHub rejected the sign-in")

var (
	loginRe     = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	numericIDRe = regexp.MustCompile(`^[1-9][0-9]{0,17}$`)
	oauthErrRe  = regexp.MustCompile(`^[a-z_]{1,40}$`)
)

// Client talks to GitHub's OAuth and user endpoints.
type Client struct {
	ClientID     string
	ClientSecret string
	// BaseURL serves /login/oauth/*; APIURL serves /user. For GitHub.com these
	// are https://github.com and https://api.github.com.
	BaseURL, APIURL *url.URL
	// HTTP should be the egress client (ADR 0008).
	HTTP *http.Client
	// Timeout bounds each request; zero means 15 seconds.
	Timeout time.Duration
	// OrgIDs, when set, are the numeric ids of the GitHub organisations whose
	// members may sign in (PRIVO_GITHUB_ORG_IDS, F05 A). Sign-in then asks
	// for read:org.
	OrgIDs []int64
}

// Profile is what Privo reads about a person: nothing else is kept or even
// decoded (no email, avatar or token).
type Profile struct {
	ID    int64  // GitHub's numeric user id: the identity
	Login string // display only: logins change and can be re-registered
	Name  string // display only; may be empty
}

// Subject is the identity key for a profile.
func (p Profile) Subject() string { return strconv.FormatInt(p.ID, 10) }

// NewVerifier returns a PKCE code verifier (RFC 7636): 43 URL-safe characters
// from 32 random bytes.
func NewVerifier() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Challenge is the S256 code challenge for a verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthorizeURL is where the browser is sent to sign in. It asks for no scope, so
// the access token can only read public profile data, and for no sign-ups;
// with OrgIDs set it asks for read:org, to read the person's organisations.
func (c *Client) AuthorizeURL(redirectURI, state, challenge string) string {
	u := c.BaseURL.JoinPath("login", "oauth", "authorize")
	q := url.Values{}
	q.Set("client_id", c.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("allow_signup", "false")
	if len(c.OrgIDs) > 0 {
		q.Set("scope", "read:org")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// Exchange trades an authorization code for an access token. The client secret
// and the code travel in the POST body, never in the URL.
func (c *Client) Exchange(ctx context.Context, code, redirectURI, verifier string) (string, error) {
	form := url.Values{}
	form.Set("client_id", c.ClientID)
	form.Set("client_secret", c.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL.JoinPath("login", "oauth", "access_token").String(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	body, status, err := c.do(req)
	if err != nil {
		return "", fmt.Errorf("githubauth: token exchange: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("githubauth: token exchange: GitHub answered with status %d", status)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", errors.New("githubauth: token exchange: the answer is not JSON")
	}
	if out.Error != "" {
		// GitHub reports a wrong or used code as a 200 with an error field.
		if oauthErrRe.MatchString(out.Error) {
			return "", fmt.Errorf("%w (%s)", ErrRejected, out.Error)
		}
		return "", ErrRejected
	}
	if out.AccessToken == "" || !strings.EqualFold(out.TokenType, "bearer") {
		return "", errors.New("githubauth: token exchange: the answer has no bearer token")
	}
	return out.AccessToken, nil
}

// User reads the profile of the person the access token belongs to.
func (c *Client) User(ctx context.Context, accessToken string) (Profile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.APIURL.JoinPath("user").String(), nil)
	if err != nil {
		return Profile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	body, status, err := c.do(req)
	if err != nil {
		return Profile{}, fmt.Errorf("githubauth: profile: %w", err)
	}
	if status != http.StatusOK {
		return Profile{}, fmt.Errorf("githubauth: profile: GitHub answered with status %d", status)
	}
	var raw struct {
		ID    json.RawMessage `json:"id"`
		Login string          `json:"login"`
		Name  *string         `json:"name"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Profile{}, errors.New("githubauth: profile: the answer is not a user object")
	}
	if !numericIDRe.Match(raw.ID) {
		return Profile{}, errors.New("githubauth: profile: no numeric user id")
	}
	id, err := strconv.ParseInt(string(raw.ID), 10, 64)
	if err != nil {
		return Profile{}, errors.New("githubauth: profile: no numeric user id")
	}
	if !loginRe.MatchString(raw.Login) {
		return Profile{}, errors.New("githubauth: profile: the login is missing or not valid")
	}
	p := Profile{ID: id, Login: raw.Login}
	if raw.Name != nil {
		p.Name = cleanName(*raw.Name)
	}
	return p, nil
}

// ErrOrgsHidden means GitHub would not show the person's organisations: the
// token lacks read:org (the person declined it) or was refused. It is not a
// failure to reach GitHub, and the person is refused as outside them.
var ErrOrgsHidden = errors.New("githubauth: GitHub did not show the organisations")

// maxOrgPages bounds how many pages of 100 organisations are read.
const maxOrgPages = 10

// Orgs returns the numeric ids of the organisations the person belongs to,
// with read:org (an organisation that restricts OAuth App access does not
// show, so it fails closed). A malformed answer is an error, not a shorter
// list.
func (c *Client) Orgs(ctx context.Context, accessToken string) ([]int64, error) {
	var out []int64
	for page := 1; page <= maxOrgPages; page++ {
		u := c.APIURL.JoinPath("user", "orgs")
		u.RawQuery = url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		body, status, err := c.do(req)
		if err != nil {
			return nil, fmt.Errorf("githubauth: organisations: %w", err)
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return nil, ErrOrgsHidden
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("githubauth: organisations: GitHub answered with status %d", status)
		}
		var orgs []struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(body, &orgs); err != nil {
			return nil, errors.New("githubauth: organisations: the answer is not a list of organisations")
		}
		for _, o := range orgs {
			if o.ID <= 0 {
				return nil, errors.New("githubauth: organisations: an organisation has no numeric id")
			}
			out = append(out, o.ID)
		}
		if len(orgs) < 100 {
			return out, nil
		}
	}
	return out, nil
}

// InOrgs reports whether ids hold one of the configured organisations.
func (c *Client) InOrgs(ids []int64) bool {
	for _, id := range ids {
		if slices.Contains(c.OrgIDs, id) {
			return true
		}
	}
	return false
}

// do sends the request with a timeout and no redirects (GitHub's endpoints do
// not redirect, and a followed 307 would send the client secret to another
// host), and reads at most maxBody bytes.
func (c *Client) do(req *http.Request) ([]byte, int, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	defer cancel()
	hc := *c.HTTP
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req.Header.Set("User-Agent", "privo-gateway")
	resp, err := hc.Do(req.WithContext(ctx))
	if err != nil {
		return nil, 0, errors.New(strings.TrimSpace(redactURL(err)))
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, 0, errors.New("reading the answer failed")
	}
	if len(b) > maxBody {
		return nil, 0, errors.New("the answer is too large")
	}
	return b, resp.StatusCode, nil
}

// redactURL describes a transport error without the request URL, which a
// *url.Error would otherwise print.
func redactURL(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return "request failed: " + err.Error()
}

// cleanName makes a display name safe to show and to log: control characters
// (newlines and tabs become spaces) and invisible format characters such as
// bidirectional overrides are removed, runs of spaces collapse, and the length
// is capped.
func cleanName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if r := []rune(out); len(r) > maxNameRunes {
		out = string(r[:maxNameRunes])
	}
	return out
}
