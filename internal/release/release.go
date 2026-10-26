// Package release knows how to ask GitHub for the newest published release
// of Proto Easy Mount and how to compare release tags. The app's updater
// uses it; it does no I/O beyond the HTTP requests it is handed.
//
// Adapted from Protomothis/smartthings-pc-control (MIT).
package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// API lists the project's releases, newest first. The list rather than
// /releases/latest lets the test channel see prereleases too, and Newest
// picks by version, not by date. 30 entries cover many months of releases.
const API = "https://api.github.com/repos/Protomothis/proto-easy-mount/releases?per_page=30"

// Page is the human-facing releases page, used when a release carries no
// html_url of its own.
const Page = "https://github.com/Protomothis/proto-easy-mount/releases"

// InstallerName is the Windows installer .github/workflows/release.yml
// attaches to the release tagged tag (v1.2.3 → ProtoEasyMount-Setup-1.2.3.exe).
func InstallerName(tag string) string {
	return "ProtoEasyMount-Setup-" + strings.TrimPrefix(tag, "v") + ".exe"
}

// ErrNoRelease: the release list holds no published release the channel
// accepts (only drafts, prereleases on the stable channel, or nothing).
var ErrNoRelease = errors.New("no published app release found")

// Asset is one file attached to a release.
type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
}

// Info is the subset of the GitHub release object we use.
type Info struct {
	TagName    string  `json:"tag_name"`
	HTMLURL    string  `json:"html_url"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
	// Body is the release notes: the tag's CHANGELOG.md section in
	// Markdown (see .github/workflows/release.yml). Notes cleans it up for
	// display.
	Body string `json:"body"`
	// PublishedAt is zero for a draft (GitHub sends null).
	PublishedAt time.Time `json:"published_at"`
}

// Latest asks GitHub for the newest published app release (see Newest). A
// nil client uses http.DefaultClient; callers normally pass one with a
// timeout.
func Latest(ctx context.Context, client *http.Client) (*Info, error) {
	return Fetch(ctx, client, API)
}

// LatestWithList is Latest that also returns the whole release list it
// decoded, for NotesSince. Still a single request.
func LatestWithList(ctx context.Context, client *http.Client) (*Info, []Info, error) {
	return FetchWithList(ctx, client, API)
}

// Fetch is Latest against an arbitrary release-list URL (tests point it at
// an httptest.Server). Any status other than 200 is an error, as is a list
// without an app release (ErrNoRelease).
func Fetch(ctx context.Context, client *http.Client, url string) (*Info, error) {
	rel, _, err := FetchWithList(ctx, client, url)
	return rel, err
}

// FetchWithList is Fetch that also returns the decoded list as GitHub sent
// it (prereleases, drafts and all). rel points into list; both are nil
// when err is set.
func FetchWithList(ctx context.Context, client *http.Client, url string) (rel *Info, list []Info, err error) {
	return FetchFor(ctx, client, url, false)
}

// FetchFor is FetchWithList on the stable (pre false) or the test channel.
func FetchFor(ctx context.Context, client *http.Client, url string, pre bool) (rel *Info, list []Info, err error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("GitHub API: HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, nil, err
	}
	rel = NewestFor(list, pre)
	if rel == nil {
		return nil, nil, ErrNoRelease
	}
	return rel, list, nil
}

// stableTag is a release tag with no suffix: v1.2.3, never v1.2.3-rc.1.
var stableTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// anyTag also takes test versions: v1.2.3-rc.1.
var anyTag = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// appRelease parses r's tag when r is a published release the channel
// takes: never a draft; on the stable channel (pre false) a plain vX.Y.Z
// that is not a prerelease, on the test channel test versions too.
func appRelease(r *Info, pre bool) (v Version, ok bool) {
	tag := stableTag
	if pre {
		tag = anyTag
	}
	if r.Draft || (r.Prerelease && !pre) || !tag.MatchString(r.TagName) {
		return Version{}, false
	}
	return ParseVersion(r.TagName)
}

// Newest returns the highest app release in list by version (not by date,
// so a hotfix for an older line published later does not win): the tag must
// be a plain vX.Y.Z and the release neither a draft nor a prerelease. nil
// when there is none.
func Newest(list []Info) *Info { return NewestFor(list, false) }

// NewestFor is Newest on the stable (pre false) or the test channel.
func NewestFor(list []Info, pre bool) *Info {
	var best *Info
	var bestV Version
	for i := range list {
		r := &list[i]
		v, ok := appRelease(r, pre)
		if !ok {
			continue
		}
		if best == nil || v.Compare(bestV) > 0 {
			best, bestV = r, v
		}
	}
	return best
}

// NotesSince returns the app releases in list (same filter as Newest) that
// are newer than current by IsNewer, highest version first: the releases
// whose notes the update dialog shows. An rc install gets its own final
// release (v1.2.1-rc3 gets v1.2.1, not v1.2.0); a build whose version does
// not parse ("dev") gets nothing, as IsNewer never offers it anything.
func NotesSince(list []Info, current string) []Info { return NotesSinceFor(list, current, false) }

// NotesSinceFor is NotesSince on the stable (pre false) or the test channel.
func NotesSinceFor(list []Info, current string, pre bool) []Info {
	type entry struct {
		v Version
		r Info
	}
	var found []entry
	for i := range list {
		v, ok := appRelease(&list[i], pre)
		if ok && IsNewer(current, list[i].TagName) {
			found = append(found, entry{v, list[i]})
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].v.Compare(found[j].v) > 0 })
	out := make([]Info, len(found))
	for i, e := range found {
		out[i] = e.r
	}
	return out
}

// versionHeading is the "## [v1.2.0] - 2026-10-03" line a CHANGELOG.md
// section, and so a release body, starts with.
var versionHeading = regexp.MustCompile(`^#{1,6}\s+\[?v?\d+\.\d+\.\d+`)

// Notes is the release body ready to show under a version heading of its
// own: a byte-order mark dropped, line endings normalised to "\n", a
// leading version heading removed and surrounding blank lines trimmed. ""
// when the release has no notes.
func (r *Info) Notes() string {
	s := strings.TrimPrefix(r.Body, string(rune(0xFEFF)))
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if first, rest, _ := strings.Cut(s, "\n"); versionHeading.MatchString(first) {
		s = strings.TrimSpace(rest)
	}
	return s
}

// InstallerAsset returns the download URL of the release's installer, or ""
// when the release carries none.
func InstallerAsset(rel *Info) string {
	if rel == nil {
		return ""
	}
	for _, a := range rel.Assets {
		if strings.EqualFold(a.Name, InstallerName(rel.TagName)) && a.DownloadURL != "" {
			return a.DownloadURL
		}
	}
	return ""
}

// Version is a parsed release tag: v1.2.3 or v1.2.3-rc4.
type Version struct {
	Major, Minor, Patch int
	// Pre is the pre-release suffix after the first "-" ("rc4"), "" for a
	// release.
	Pre string
}

// ParseVersion parses "v1.2.3" or "v1.2.3-rc4" (the leading v is optional).
// ok is false for non-release builds ("dev", "") so they never trigger
// update prompts, and for anything else that is not three numbers with an
// optional suffix of letters, digits, dots and dashes.
func ParseVersion(v string) (ver Version, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	core, pre, hasPre := strings.Cut(v, "-")
	if hasPre && !validPre(pre) {
		return Version{}, false
	}
	fields := strings.Split(core, ".")
	if len(fields) != 3 {
		return Version{}, false
	}
	var n [3]int
	for i, f := range fields {
		if f == "" || strings.Trim(f, "0123456789") != "" {
			return Version{}, false
		}
		x, err := strconv.Atoi(f)
		if err != nil {
			return Version{}, false
		}
		n[i] = x
	}
	return Version{Major: n[0], Minor: n[1], Patch: n[2], Pre: pre}, true
}

func validPre(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

// String renders v as a tag: v1.2.3 or v1.2.3-rc4.
func (v Version) String() string {
	s := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Compare returns -1, 0 or +1 as v is older than, equal to or newer than o.
// Numbers compare numerically; at equal numbers any pre-release is older
// than the release (v1.2.0-rc3 < v1.2.0, also for unknown suffixes), and
// two pre-releases compare by comparePre (rc2 < rc10, beta < rc).
func (v Version) Compare(o Version) int {
	for _, d := range [3]int{v.Major - o.Major, v.Minor - o.Minor, v.Patch - o.Patch} {
		switch {
		case d < 0:
			return -1
		case d > 0:
			return 1
		}
	}
	switch {
	case v.Pre == o.Pre:
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	}
	return comparePre(v.Pre, o.Pre)
}

// comparePre orders two non-empty pre-release suffixes. Each is split into
// runs of digits and of other characters ("rc10" -> "rc", 10; "rc.2" ->
// "rc", 2; dots and dashes only separate). Runs compare pairwise: numbers
// numerically, words case-insensitively, a number before a word (as in
// semver); when one is a prefix of the other the shorter is older.
func comparePre(a, b string) int {
	ta, tb := preTokens(a), preTokens(b)
	for i := 0; i < len(ta) && i < len(tb); i++ {
		x, y := ta[i], tb[i]
		xn, xNum := atoiDigits(x)
		yn, yNum := atoiDigits(y)
		switch {
		case xNum && yNum:
			if xn != yn {
				if xn < yn {
					return -1
				}
				return 1
			}
		case xNum:
			return -1
		case yNum:
			return 1
		default:
			if c := strings.Compare(strings.ToLower(x), strings.ToLower(y)); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(ta) < len(tb):
		return -1
	case len(ta) > len(tb):
		return 1
	}
	return 0
}

func preTokens(s string) []string {
	var out []string
	start := -1
	digit := false
	flush := func(end int) {
		if start >= 0 {
			out = append(out, s[start:end])
			start = -1
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '.' || c == '-' {
			flush(i)
			continue
		}
		d := c >= '0' && c <= '9'
		if start >= 0 && d != digit {
			flush(i)
		}
		if start < 0 {
			start, digit = i, d
		}
	}
	flush(len(s))
	return out
}

// atoiDigits parses an all-digit token; ok is false for words and for
// numbers too large for an int (those then compare as words).
func atoiDigits(s string) (int, bool) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// Compare parses both tags and compares them (see Version.Compare). ok is
// false when either side fails ParseVersion.
func Compare(a, b string) (c int, ok bool) {
	va, ok := ParseVersion(a)
	if !ok {
		return 0, false
	}
	vb, ok := ParseVersion(b)
	if !ok {
		return 0, false
	}
	return va.Compare(vb), true
}

// IsNewer reports whether latest is a higher version than current, counting
// pre-releases as older than their release: an installed v1.2.0-rc4 sees
// v1.2.0 as newer. Either side failing ParseVersion yields false.
func IsNewer(current, latest string) bool {
	c, ok := Compare(latest, current)
	return ok && c > 0
}

// Repo and Sponsor are the project's pages, linked from the app.
const (
	Repo    = "https://github.com/Protomothis/proto-easy-mount"
	Sponsor = "https://github.com/sponsors/Protomothis"
)
