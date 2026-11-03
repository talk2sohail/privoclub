package catalog

import (
	"net/url"
	"regexp"
	"slices"
	"testing"
	"time"
)

// The same pattern the admin API requires of a server name.
var aliasRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,15}$`)

func TestCatalogEntriesAreValid(t *testing.T) {
	profiles := map[string]bool{}
	for _, p := range Profiles() {
		profiles[p.ID] = true
	}
	ids, aliases, urls := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, e := range Entries() {
		if e.ID == "" || ids[e.ID] {
			t.Errorf("entry id %q is empty or repeated", e.ID)
		}
		ids[e.ID] = true
		if !aliasRe.MatchString(e.Alias) || aliases[e.Alias] {
			t.Errorf("%s: alias %q is invalid or repeated", e.ID, e.Alias)
		}
		aliases[e.Alias] = true
		u, err := url.Parse(e.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || urls[e.URL] {
			t.Errorf("%s: URL %q must be https, without credentials or a query, and unique", e.ID, e.URL)
		}
		urls[e.URL] = true
		if e.Transport != "http" && e.Transport != "sse" {
			t.Errorf("%s: transport %q", e.ID, e.Transport)
		}
		switch e.Auth {
		case AuthNone:
			if e.Registration != "" || e.AppURL != "" || len(e.Scopes) > 0 {
				t.Errorf("%s: a server without sign-in has OAuth settings", e.ID)
			}
		case AuthOAuth:
			if e.Registration != RegistrationAutomatic && e.Registration != RegistrationOwnApp {
				t.Errorf("%s: registration %q", e.ID, e.Registration)
			}
			if e.Registration == RegistrationOwnApp && e.AppHelp == "" {
				t.Errorf("%s: needs its own OAuth app but says nothing about it", e.ID)
			}
			if e.AppURL != "" {
				if a, err := url.Parse(e.AppURL); err != nil || a.Scheme != "https" {
					t.Errorf("%s: app URL %q", e.ID, e.AppURL)
				}
			}
		default:
			t.Errorf("%s: auth %q", e.ID, e.Auth)
		}
		if len(e.Profiles) == 0 {
			t.Errorf("%s: no profile", e.ID)
		}
		for _, p := range e.Profiles {
			if !profiles[p] {
				t.Errorf("%s: unknown profile %q", e.ID, p)
			}
		}
		if _, err := time.Parse(time.DateOnly, e.CheckedOn); err != nil || e.Name == "" || e.Description == "" {
			t.Errorf("%s: needs a name, a description and the date it was checked", e.ID)
		}
		if got, ok := ByID(e.ID); !ok || got.URL != e.URL {
			t.Errorf("ByID(%q) = %v %v", e.ID, got, ok)
		}
	}
	if _, ok := ByID("nope"); ok {
		t.Error("ByID found an unknown id")
	}
	if !slices.IsSortedFunc(Entries(), func(a, b Entry) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	}) {
		t.Error("Entries is not sorted by name")
	}
}

func TestProfilesHaveEntries(t *testing.T) {
	for _, p := range Profiles() {
		n := 0
		for _, e := range Entries() {
			if slices.Contains(e.Profiles, p.ID) {
				n++
			}
		}
		if n == 0 {
			t.Errorf("profile %q has no entry", p.ID)
		}
	}
}

func TestEntriesIsACopy(t *testing.T) {
	a := Entries()
	a[0].URL = "https://evil.example/"
	if Entries()[0].URL == "https://evil.example/" {
		t.Fatal("callers can change the catalog")
	}
}
