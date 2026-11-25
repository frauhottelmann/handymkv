package hmkv

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TheDiscDB (https://thediscdb.com) is a community database that maps the titles of a disc
// (as MakeMKV sees them) to the movie, episode or extra they contain. handymkv uses its public
// GraphQL API to show the real name of each title in the selection list and to name the ripped
// files after it.

const (
	defaultDiscDBEndpoint = "https://thediscdb.com/graphql/"
	discDBRequestTimeout  = 20 * time.Second
)

// ErrDiscDBNoMatch is returned when TheDiscDB does not know the disc.
var ErrDiscDBNoMatch = errors.New("disc not found in TheDiscDB")

// DiscDBClient talks to the public TheDiscDB GraphQL endpoint.
type DiscDBClient struct {
	Endpoint   string
	APIKey     string
	UserAgent  string
	HTTPClient *http.Client
}

func NewDiscDBClient(apiKey, userAgent string) *DiscDBClient {
	return &DiscDBClient{
		Endpoint:   defaultDiscDBEndpoint,
		APIKey:     apiKey,
		UserAgent:  userAgent,
		HTTPClient: &http.Client{Timeout: discDBRequestTimeout},
	}
}

// Data model of the GraphQL responses (only the fields handymkv requests).

type discDBItem struct {
	Title   string `json:"title"`
	Type    string `json:"type"`
	Season  string `json:"season"`
	Episode string `json:"episode"`
}

type discDBTitle struct {
	Index      int         `json:"index"`
	SourceFile string      `json:"sourceFile"`
	SegmentMap string      `json:"segmentMap"`
	Duration   string      `json:"duration"`
	Size       int64       `json:"size"`
	Item       *discDBItem `json:"item"`
}

type discDBDisc struct {
	Index       int           `json:"index"`
	Name        string        `json:"name"`
	Format      string        `json:"format"`
	ContentHash string        `json:"contentHash"`
	Titles      []discDBTitle `json:"titles"`
}

type discDBRelease struct {
	Title string       `json:"title"`
	Year  int          `json:"year"`
	Discs []discDBDisc `json:"discs"`
}

type discDBMediaItem struct {
	Title    string          `json:"title"`
	Year     int             `json:"year"`
	Type     string          `json:"type"`
	Releases []discDBRelease `json:"releases"`
}

type discDBBoxset struct {
	Title   string         `json:"title"`
	Release *discDBRelease `json:"release"`
}

type discDBQueryData struct {
	MediaItems *struct {
		Nodes []discDBMediaItem `json:"nodes"`
	} `json:"mediaItems"`
	Boxsets *struct {
		Nodes []discDBBoxset `json:"nodes"`
	} `json:"boxsets"`
}

// DiscDBMatch is one disc of one release in TheDiscDB.
type DiscDBMatch struct {
	// Title of the movie or series, or of the boxset when the disc belongs to one.
	MediaTitle string
	MediaYear  int
	MediaType  string
	// Title of the release (e.g. "Season 1 Blu-ray").
	ReleaseTitle string
	Disc         discDBDisc

	// release numbers the releases of one response, so discs of the same release can be grouped.
	release int
}

func (m DiscDBMatch) String() string {
	var sb strings.Builder
	sb.WriteString(m.MediaTitle)
	if m.MediaYear > 0 {
		fmt.Fprintf(&sb, " (%d)", m.MediaYear)
	}
	if m.ReleaseTitle != "" {
		fmt.Fprintf(&sb, " - %s", m.ReleaseTitle)
	}
	disc := m.Disc.Name
	if disc == "" {
		disc = fmt.Sprintf("Disc %d", m.Disc.Index)
	}
	fmt.Fprintf(&sb, " - %s", disc)
	if m.Disc.Format != "" {
		fmt.Fprintf(&sb, " [%s]", m.Disc.Format)
	}
	return sb.String()
}

const discDBTitleFields = `
	index
	name
	format
	contentHash
	titles(order: { index: ASC }) {
		index
		sourceFile
		segmentMap
		duration
		size
		item { title type season episode }
	}`

var discDBHashQuery = discDBLookupQuery("HandyMKVDiscByHash", "contentHash")

// discDBDiscIDQuery looks a disc up by its "Disc ID", the global disc ID that MakeMKV reports
// (not the same as the content hash, which TheDiscDB shows as "Disc Hash").
var discDBDiscIDQuery = discDBLookupQuery("HandyMKVDiscByID", "globalDiscId")

func discDBLookupQuery(name, field string) string {
	return strings.NewReplacer("HandyMKVDiscByHash", name, "contentHash: {", field+": {").Replace(discDBHashQueryTemplate)
}

var discDBHashQueryTemplate = `query HandyMKVDiscByHash($hash: String) {
  mediaItems(where: { releases: { some: { discs: { some: { contentHash: { eq: $hash } } } } } }) {
    nodes {
      title
      year
      type
      releases(where: { discs: { some: { contentHash: { eq: $hash } } } }) {
        title
        year
        discs(where: { contentHash: { eq: $hash } }) {` + discDBTitleFields + `
        }
      }
    }
  }
  boxsets(where: { release: { discs: { some: { contentHash: { eq: $hash } } } } }) {
    nodes {
      title
      release {
        title
        year
        discs(where: { contentHash: { eq: $hash } }) {` + discDBTitleFields + `
        }
      }
    }
  }
}`

var discDBMediaSearchQuery = `query HandyMKVSearchMedia($term: String) {
  mediaItems(where: { title: { contains: $term } }, first: 25, order: { title: ASC }) {
    nodes {
      title
      year
      type
    }
  }
  boxsets(where: { title: { contains: $term } }, first: 10) {
    nodes {
      title
      release {
        title
        year
      }
    }
  }
}`

var discDBMediaItemDiscsQuery = `query HandyMKVMediaItemDiscs($title: String) {
  mediaItems(where: { title: { eq: $title } }) {
    nodes {
      title
      year
      type
      releases(order: { year: ASC }) {
        title
        year
        discs(order: { index: ASC }) {` + discDBTitleFields + `
        }
      }
    }
  }
}`

var discDBBoxsetDiscsQuery = `query HandyMKVBoxsetDiscs($title: String) {
  boxsets(where: { title: { eq: $title } }) {
    nodes {
      title
      release {
        title
        year
        discs(order: { index: ASC }) {` + discDBTitleFields + `
        }
      }
    }
  }
}`

func (c *DiscDBClient) query(ctx context.Context, query string, variables map[string]any) (*discDBQueryData, error) {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "ApiKey "+c.APIKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("TheDiscDB request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("reading TheDiscDB response failed: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("TheDiscDB rejected the request (HTTP %d) - an API key may be required (config: thediscdb_api_key)", resp.StatusCode)
	}

	var result struct {
		Data   *discDBQueryData `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("TheDiscDB returned HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("could not parse TheDiscDB response: %w", err)
	}

	if len(result.Errors) > 0 {
		msgs := make([]string, 0, len(result.Errors))
		for _, e := range result.Errors {
			msgs = append(msgs, e.Message)
		}
		return nil, fmt.Errorf("TheDiscDB returned errors: %s", strings.Join(msgs, "; "))
	}

	if result.Data == nil {
		return nil, fmt.Errorf("TheDiscDB returned no data (HTTP %d)", resp.StatusCode)
	}

	return result.Data, nil
}

// flattenDiscDBMatches turns the nested GraphQL response into a flat list of discs.
// If onlyHash is set, discs with a different content hash are dropped.
func flattenDiscDBMatches(data *discDBQueryData, onlyHash string) []DiscDBMatch {
	matches := make([]DiscDBMatch, 0)

	keep := func(d discDBDisc) bool {
		return onlyHash == "" || strings.EqualFold(d.ContentHash, onlyHash)
	}

	release := 0

	if data.MediaItems != nil {
		for _, mi := range data.MediaItems.Nodes {
			for _, rel := range mi.Releases {
				release++
				for _, d := range rel.Discs {
					if !keep(d) {
						continue
					}
					matches = append(matches, DiscDBMatch{
						MediaTitle:   mi.Title,
						MediaYear:    mi.Year,
						MediaType:    mi.Type,
						ReleaseTitle: rel.Title,
						Disc:         d,
						release:      release,
					})
				}
			}
		}
	}

	if data.Boxsets != nil {
		for _, bs := range data.Boxsets.Nodes {
			if bs.Release == nil {
				continue
			}
			release++
			for _, d := range bs.Release.Discs {
				if !keep(d) {
					continue
				}
				matches = append(matches, DiscDBMatch{
					MediaTitle:   bs.Title,
					MediaYear:    bs.Release.Year,
					MediaType:    "Boxset",
					ReleaseTitle: bs.Release.Title,
					Disc:         d,
					release:      release,
				})
			}
		}
	}

	return matches
}

// LookupByHash returns all discs in TheDiscDB with the given content hash. The same disc can be
// part of several releases (e.g. a single season and a complete-series boxset).
// LookupByDiscID returns all discs in TheDiscDB with the given disc ID (TheDiscDB's "Disc ID", the
// global disc ID reported by MakeMKV).
func (c *DiscDBClient) LookupByDiscID(ctx context.Context, id string) ([]DiscDBMatch, error) {
	data, err := c.query(ctx, discDBDiscIDQuery, map[string]any{"hash": strings.ToUpper(id)})
	if err != nil {
		return nil, err
	}

	// The query only returns discs with this ID.
	matches := flattenDiscDBMatches(data, "")
	if len(matches) == 0 {
		return nil, ErrDiscDBNoMatch
	}

	return matches, nil
}

func (c *DiscDBClient) LookupByHash(ctx context.Context, hash string) ([]DiscDBMatch, error) {
	hash = strings.ToUpper(hash)
	data, err := c.query(ctx, discDBHashQuery, map[string]any{"hash": hash})
	if err != nil {
		return nil, err
	}

	matches := flattenDiscDBMatches(data, hash)
	if len(matches) == 0 {
		return nil, ErrDiscDBNoMatch
	}

	return matches, nil
}

// DiscDBMedia is a movie, series or boxset found by a title search.
type DiscDBMedia struct {
	Title string
	Year  int
	// TheDiscDB's media type ("Movie", "Series") or "Boxset".
	Type string
}

func (m DiscDBMedia) String() string {
	var sb strings.Builder
	sb.WriteString(m.Title)
	if m.Year > 0 {
		fmt.Fprintf(&sb, " (%d)", m.Year)
	}
	if m.Type != "" {
		fmt.Fprintf(&sb, " [%s]", m.Type)
	}
	return sb.String()
}

// SearchMedia returns the movies, series and boxsets whose title contains the term. It only
// fetches the titles; the discs of the chosen entry are loaded with MediaDiscs.
func (c *DiscDBClient) SearchMedia(ctx context.Context, term string) ([]DiscDBMedia, error) {
	data, err := c.query(ctx, discDBMediaSearchQuery, map[string]any{"term": term})
	if err != nil {
		return nil, err
	}

	result := make([]DiscDBMedia, 0)
	seen := make(map[DiscDBMedia]struct{})
	add := func(m DiscDBMedia) {
		if _, dup := seen[m]; dup || strings.TrimSpace(m.Title) == "" {
			return
		}
		seen[m] = struct{}{}
		result = append(result, m)
	}

	if data.MediaItems != nil {
		for _, mi := range data.MediaItems.Nodes {
			add(DiscDBMedia{Title: mi.Title, Year: mi.Year, Type: mi.Type})
		}
	}
	if data.Boxsets != nil {
		for _, bs := range data.Boxsets.Nodes {
			m := DiscDBMedia{Title: bs.Title, Type: "Boxset"}
			if bs.Release != nil {
				m.Year = bs.Release.Year
			}
			add(m)
		}
	}

	return result, nil
}

// MediaDiscs returns all discs of all releases of a movie, series or boxset found by SearchMedia.
func (c *DiscDBClient) MediaDiscs(ctx context.Context, media DiscDBMedia) ([]DiscDBMatch, error) {
	query := discDBMediaItemDiscsQuery
	if media.Type == "Boxset" {
		query = discDBBoxsetDiscsQuery
	}

	data, err := c.query(ctx, query, map[string]any{"title": media.Title})
	if err != nil {
		return nil, err
	}

	// Titles are not unique (e.g. remakes), so keep only the entry that was chosen.
	all := flattenDiscDBMatches(data, "")
	matches := make([]DiscDBMatch, 0, len(all))
	for _, m := range all {
		if m.MediaType == media.Type && (media.Year == 0 || m.MediaYear == media.Year) {
			matches = append(matches, m)
		}
	}

	return matches, nil
}

// ---------------------------------------------------------------------------
// Content hash
// ---------------------------------------------------------------------------

// ComputeDiscContentHash computes TheDiscDB's "content hash" of a mounted disc.
//
// It mirrors TheDiscDB's own implementation (DiscFileCatalog.ComputeContentHash in
// github.com/TheDiscDb/web): an MD5 over the sizes (int64, little endian) of
//   - Blu-ray/UHD: every BDMV/STREAM/*.m2ts file (not in subdirectories)
//   - DVD:         every VIDEO_TS/*.VOB, *.IFO and *.BUP file
//
// sorted by file name, rendered as upper case hex.
func ComputeDiscContentHash(root string) (string, error) {
	type file struct {
		name string
		size int64
	}

	collect := func(dir string, match func(name string) bool) ([]file, error) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		files := make([]file, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() || !match(strings.ToLower(e.Name())) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				return nil, err
			}
			files = append(files, file{name: e.Name(), size: info.Size()})
		}
		return files, nil
	}

	var files []file
	var err error

	if dir, ok := findDirCaseInsensitive(root, "BDMV", "STREAM"); ok {
		files, err = collect(dir, func(n string) bool { return strings.HasSuffix(n, ".m2ts") })
	} else if dir, ok := findDirCaseInsensitive(root, "VIDEO_TS"); ok {
		files, err = collect(dir, func(n string) bool {
			return strings.HasSuffix(n, ".vob") || strings.HasSuffix(n, ".ifo") || strings.HasSuffix(n, ".bup")
		})
	} else {
		return "", fmt.Errorf("no BDMV/STREAM or VIDEO_TS directory found in %s", root)
	}

	if err != nil {
		return "", err
	}

	if len(files) == 0 {
		return "", fmt.Errorf("no video files found on disc at %s", root)
	}

	// TheDiscDB sorts ordinally by the file name as stored on the disc, which is upper case for
	// DVDs and digits only for Blu-rays. Some systems present ISO9660 names in lower case, so
	// compare upper case names to get the same order everywhere.
	sort.Slice(files, func(i, j int) bool {
		return strings.ToUpper(files[i].name) < strings.ToUpper(files[j].name)
	})

	h := md5.New()
	buf := make([]byte, 8)
	for _, f := range files {
		binary.LittleEndian.PutUint64(buf, uint64(f.size))
		h.Write(buf)
	}

	return strings.ToUpper(hex.EncodeToString(h.Sum(nil))), nil
}

// findDirCaseInsensitive resolves a path below root, matching each element case-insensitively.
func findDirCaseInsensitive(root string, elems ...string) (string, bool) {
	cur := root
	for _, elem := range elems {
		entries, err := os.ReadDir(cur)
		if err != nil {
			return "", false
		}
		found := false
		for _, e := range entries {
			if e.IsDir() && strings.EqualFold(e.Name(), elem) {
				cur = filepath.Join(cur, e.Name())
				found = true
				break
			}
		}
		if !found {
			return "", false
		}
	}
	return cur, true
}

// ---------------------------------------------------------------------------
// Title matching
// ---------------------------------------------------------------------------

// normalizeDuration turns "1:02:03" / "01:02:03" into seconds; returns -1 if unparseable.
func normalizeDuration(d string) int {
	parts := strings.Split(strings.TrimSpace(d), ":")
	if len(parts) == 0 || len(parts) > 3 {
		return -1
	}
	total := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return -1
		}
		total = total*60 + n
	}
	return total
}

func normalizeSegmentMap(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), " ", "")
}

// matchDiscDBTitle finds the TheDiscDB title that corresponds to a MakeMKV title.
//
// TheDiscDB records its titles from MakeMKV logs, so the strongest key is MakeMKV's source file
// (e.g. "00800.mpls" on Blu-ray, the title number on DVD). Playlists can be reused with different
// segment maps, so the segment map and the size break ties. Titles without a source file
// fall back to an exact size match.
func matchDiscDBTitle(t *TitleInfo, candidates []discDBTitle) *discDBTitle {
	var pool []*discDBTitle

	if t.SourceFile != "" {
		for i := range candidates {
			if strings.EqualFold(strings.TrimSpace(candidates[i].SourceFile), t.SourceFile) {
				pool = append(pool, &candidates[i])
			}
		}
	}

	if len(pool) == 0 {
		// No source file information - only accept an exact size match.
		if t.FileSizeBytes <= 0 {
			return nil
		}
		for i := range candidates {
			if candidates[i].Size == int64(t.FileSizeBytes) {
				pool = append(pool, &candidates[i])
			}
		}
		if len(pool) == 0 {
			return nil
		}
	}

	narrow := func(pred func(c *discDBTitle) bool) {
		if len(pool) < 2 {
			return
		}
		var next []*discDBTitle
		for _, c := range pool {
			if pred(c) {
				next = append(next, c)
			}
		}
		if len(next) > 0 {
			pool = next
		}
	}

	if t.SegmentMap != "" {
		narrow(func(c *discDBTitle) bool {
			return normalizeSegmentMap(c.SegmentMap) == normalizeSegmentMap(t.SegmentMap)
		})
	}
	if t.FileSizeBytes > 0 {
		narrow(func(c *discDBTitle) bool { return c.Size == int64(t.FileSizeBytes) })
	}
	if d := normalizeDuration(t.Length); d >= 0 {
		narrow(func(c *discDBTitle) bool { return normalizeDuration(c.Duration) == d })
	}
	narrow(func(c *discDBTitle) bool { return c.Index == t.Index })

	return pool[0]
}

// ApplyDiscDBMatch attaches TheDiscDB names to the MakeMKV titles. It returns the number of
// titles that received a name.
func ApplyDiscDBMatch(titles []TitleInfo, match DiscDBMatch) int {
	named := 0
	used := make(map[string]int)

	for i := range titles {
		titles[i].DiscDBName = ""
		titles[i].DiscDBType = ""
		titles[i].DiscDBFileName = ""
		titles[i].DiscDBShortName = ""
		titles[i].DiscDBSeries = ""
		titles[i].DiscDBSeason = 0
		titles[i].discDBTitleIndex = -1

		dt := matchDiscDBTitle(&titles[i], match.Disc.Titles)
		if dt == nil || dt.Item == nil || strings.TrimSpace(dt.Item.Title) == "" {
			continue
		}

		titles[i].DiscDBName = discDBDisplayName(match, dt.Item)
		titles[i].DiscDBType = dt.Item.Type
		titles[i].discDBTitleIndex = dt.Index
		titles[i].DiscDBShortName = discDBShortName(match, dt.Item)

		if strings.EqualFold(dt.Item.Type, "Episode") {
			if series := strings.TrimSpace(match.MediaTitle); series != "" {
				season, err := strconv.Atoi(strings.TrimSpace(dt.Item.Season))
				if err != nil || season < 0 {
					season = match.Season()
				}
				if season >= 0 {
					titles[i].DiscDBSeries = series
					titles[i].DiscDBSeason = season
				}
			}
		}

		base := sanitizeFileName(discDBBaseName(match, dt.Item))
		if base == "" {
			continue
		}

		// Avoid two titles being renamed to the same file.
		key := strings.ToLower(base)
		used[key]++
		if used[key] > 1 {
			base = fmt.Sprintf("%s (%d)", base, used[key])
		}

		titles[i].DiscDBFileName = base + ".mkv"
		named++
	}

	return named
}

// DiscDBTitleTypes are the item types TheDiscDB assigns to the titles of a disc, as defined by
// its data validation (housekeeping/appliances/check-summaries.ts in github.com/TheDiscDb/data).
var DiscDBTitleTypes = []string{
	"MainMovie",
	"Episode",
	"Extra",
	"Trailer",
	"DeletedScene",
	"Other",
	"Interview",
	"Featurette",
	"Scene",
	"Music",
	"Short",
}

// DefaultAutoSelectTypes are selected automatically if no types are configured: the main movie of
// a movie disc and the episodes of a series disc.
var DefaultAutoSelectTypes = []string{"MainMovie", "Episode"}

// ParseDiscDBTitleTypes turns a list of type names (any case) or 1-based numbers into
// TheDiscDB's spelling of the types. Empty entries and duplicates are dropped.
func ParseDiscDBTitleTypes(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{})

	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}

		canonical := ""
		if n, err := strconv.Atoi(v); err == nil {
			if n >= 1 && n <= len(DiscDBTitleTypes) {
				canonical = DiscDBTitleTypes[n-1]
			}
		} else {
			for _, t := range DiscDBTitleTypes {
				if strings.EqualFold(t, v) {
					canonical = t
					break
				}
			}
		}

		if canonical == "" {
			return nil, fmt.Errorf("unknown TheDiscDB title type '%s' (valid types: %s)", v, strings.Join(DiscDBTitleTypes, ", "))
		}

		if _, dup := seen[canonical]; dup {
			continue
		}
		seen[canonical] = struct{}{}
		result = append(result, canonical)
	}

	return result, nil
}

// SelectDiscDBTitlesByType returns the titles that TheDiscDB identified as one of the given types
// (compared case-insensitively), in disc order. If several MakeMKV titles match the same
// TheDiscDB title (e.g. the same playlist listed twice), only the first one is returned, so
// nothing is ripped twice. The result is empty if the disc was not identified or contains no
// title of these types.
func SelectDiscDBTitlesByType(titles []TitleInfo, types []string) []TitleInfo {
	selected := make([]TitleInfo, 0)
	seen := make(map[int]struct{})

	for _, t := range titles {
		if t.DiscDBType == "" || !slices.ContainsFunc(types, func(s string) bool { return strings.EqualFold(s, t.DiscDBType) }) {
			continue
		}
		if _, dup := seen[t.discDBTitleIndex]; dup {
			continue
		}
		seen[t.discDBTitleIndex] = struct{}{}
		selected = append(selected, t)
	}

	return selected
}

func formatSeasonEpisode(season, episode string) string {
	s, errS := strconv.Atoi(strings.TrimSpace(season))
	e, errE := strconv.Atoi(strings.TrimSpace(episode))
	if errS == nil && errE == nil {
		return fmt.Sprintf("S%02dE%02d", s, e)
	}
	if errE == nil && strings.TrimSpace(season) == "" {
		return fmt.Sprintf("E%02d", e)
	}
	if strings.TrimSpace(season) == "" && strings.TrimSpace(episode) == "" {
		return ""
	}
	return fmt.Sprintf("S%sE%s", strings.TrimSpace(season), strings.TrimSpace(episode))
}

// discDBDisplayName is the name shown in the title selection list.
func discDBDisplayName(match DiscDBMatch, item *discDBItem) string {
	name := discDBBaseName(match, item)
	if item.Type != "" && !strings.EqualFold(item.Type, "Episode") && !strings.EqualFold(item.Type, "MainMovie") {
		name = fmt.Sprintf("%s [%s]", name, item.Type)
	}
	return name
}

// discDBShortName is the name without the movie/series prefix, for the progress table.
//
//	Episode:    "S01E02 - <Episode title>"
//	Main movie: "<Movie title> (<Year>)"
//	Other:      "<Item title>"
func discDBShortName(match DiscDBMatch, item *discDBItem) string {
	itemTitle := strings.TrimSpace(item.Title)

	switch {
	case strings.EqualFold(item.Type, "Episode"):
		if se := formatSeasonEpisode(item.Season, item.Episode); se != "" {
			return se + " - " + itemTitle
		}
		return itemTitle
	case strings.EqualFold(item.Type, "MainMovie"):
		return discDBBaseName(match, item)
	default:
		return itemTitle
	}
}

// discDBBaseName builds the output file name (without extension):
//
//	Episode:    "<Series> - S01E02 - <Episode title>"
//	Main movie: "<Movie title> (<Year>)"
//	Other:      "<Movie/Series> - <Item title>"
func discDBBaseName(match DiscDBMatch, item *discDBItem) string {
	itemTitle := strings.TrimSpace(item.Title)
	media := strings.TrimSpace(match.MediaTitle)

	switch {
	case strings.EqualFold(item.Type, "Episode"):
		se := formatSeasonEpisode(item.Season, item.Episode)
		parts := make([]string, 0, 3)
		if media != "" && !strings.EqualFold(media, itemTitle) {
			parts = append(parts, media)
		}
		if se != "" {
			parts = append(parts, se)
		}
		parts = append(parts, itemTitle)
		return strings.Join(parts, " - ")
	case strings.EqualFold(item.Type, "MainMovie"):
		if match.MediaYear > 0 && match.MediaType != "Boxset" {
			return fmt.Sprintf("%s (%d)", itemTitle, match.MediaYear)
		}
		return itemTitle
	default:
		if media != "" && !strings.EqualFold(media, itemTitle) {
			return fmt.Sprintf("%s - %s", media, itemTitle)
		}
		return itemTitle
	}
}

// sanitizeFileName removes characters that are not allowed in file names on Windows, macOS or
// Linux and trims the result to a sensible length.
func sanitizeFileName(name string) string {
	var sb strings.Builder
	for _, r := range name {
		switch {
		case r < 32:
			continue
		case strings.ContainsRune(`<>:"/\|?*`, r):
			if r == ':' {
				sb.WriteString(" -")
			} else {
				sb.WriteRune(' ')
			}
		default:
			sb.WriteRune(r)
		}
	}

	result := strings.Join(strings.Fields(sb.String()), " ")
	result = strings.TrimRight(result, ". ")

	const maxLen = 180
	if len(result) > maxLen {
		// cut on a rune boundary
		cut := 0
		for i := range result {
			if i > maxLen {
				break
			}
			cut = i
		}
		result = strings.TrimRight(result[:cut], ". ")
	}

	// Reserved device names on Windows
	switch strings.ToUpper(result) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		result = "_" + result
	}

	return result
}

// ---------------------------------------------------------------------------
// Interactive identification
// ---------------------------------------------------------------------------

// identifyTitlesWithDiscDB looks the disc up in TheDiscDB and attaches the names to the titles.
// Every failure is non-fatal: the titles simply keep the MakeMKV names.
func identifyTitlesWithDiscDB(client *DiscDBClient, titles []TitleInfo) {
	if client == nil || len(titles) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*discDBRequestTimeout)
	defer cancel()

	fmt.Printf("Looking up disc in TheDiscDB...\n")

	match, err := lookupDiscByHash(ctx, client, titles[0].DevicePath)
	if err != nil {
		fmt.Printf("TheDiscDB: %v\n\n", err)
		match = searchDiscDBInteractive(client, titles)
		if match == nil {
			fmt.Println()
			return
		}
	}

	named := ApplyDiscDBMatch(titles, *match)
	fmt.Printf("TheDiscDB: %s - %d of %d titles identified.\n\n", match.String(), named, len(titles))
}

func lookupDiscByHash(ctx context.Context, client *DiscDBClient, devicePath string) (*DiscDBMatch, error) {
	mountPoint, err := findDiscMountPoint(devicePath)
	if err != nil {
		return nil, fmt.Errorf("could not access the disc file system (%w)", err)
	}

	hash, err := ComputeDiscContentHash(mountPoint)
	if err != nil {
		return nil, fmt.Errorf("could not compute the disc hash (%w)", err)
	}

	matches, err := client.LookupByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, ErrDiscDBNoMatch) {
			return nil, fmt.Errorf("no disc with ID %s found", hash)
		}
		return nil, err
	}

	// All matches share the same content hash and therefore the same titles. A disc can be part
	// of several releases; movie/series releases come first and give better names than boxsets.
	return &matches[0], nil
}

// discDBSeasonPattern finds a season number in a release or disc name, e.g. "Season 2 Blu-ray".
var discDBSeasonPattern = regexp.MustCompile(`(?i)\b(?:season|staffel|saison|temporada|series)\s*0*(\d+)`)

// Season returns the season of the disc, or -1 if it is unknown (e.g. a movie). It is the
// season of most episodes on the disc; discs without episodes (bonus discs) take the season from
// the release or disc name.
func (m DiscDBMatch) Season() int {
	counts := make(map[int]int)
	for _, t := range m.Disc.Titles {
		if t.Item == nil || !strings.EqualFold(t.Item.Type, "Episode") {
			continue
		}
		if s, err := strconv.Atoi(strings.TrimSpace(t.Item.Season)); err == nil && s >= 0 {
			counts[s]++
		}
	}

	season, best := -1, 0
	for s, n := range counts {
		if n > best || (n == best && s < season) {
			season, best = s, n
		}
	}
	if season >= 0 {
		return season
	}

	for _, name := range []string{m.ReleaseTitle, m.Disc.Name} {
		if sm := discDBSeasonPattern.FindStringSubmatch(name); sm != nil {
			if s, err := strconv.Atoi(sm[1]); err == nil {
				return s
			}
		}
	}

	return -1
}

// discDBSeasons returns the known seasons of the discs, in ascending order.
func discDBSeasons(discs []DiscDBMatch) []int {
	seasons := make([]int, 0)
	for _, d := range discs {
		if s := d.Season(); s >= 0 && !slices.Contains(seasons, s) {
			seasons = append(seasons, s)
		}
	}
	slices.Sort(seasons)
	return seasons
}

// discDBReleaseDiscs groups the discs by release (in the order of the releases) and sorts the
// discs of each release by their index. If season is >= 0, only discs of that season are kept,
// so the n-th disc of a group is "disc n" of that season, even in a complete-series boxset.
func discDBReleaseDiscs(discs []DiscDBMatch, season int) [][]DiscDBMatch {
	groups := make([][]DiscDBMatch, 0)
	pos := make(map[int]int)

	for _, d := range discs {
		if season >= 0 && d.Season() != season {
			continue
		}
		i, ok := pos[d.release]
		if !ok {
			i = len(groups)
			pos[d.release] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], d)
	}

	for _, g := range groups {
		sort.SliceStable(g, func(a, b int) bool { return g[a].Disc.Index < g[b].Disc.Index })
	}

	return groups
}

// discDBDiscCandidates returns disc number n (1-based) of every release.
func discDBDiscCandidates(groups [][]DiscDBMatch, n int) []DiscDBMatch {
	result := make([]DiscDBMatch, 0, len(groups))
	for _, g := range groups {
		if n >= 1 && n <= len(g) {
			result = append(result, g[n-1])
		}
	}
	return result
}

// rankDiscDBMatches sorts the discs by the number of MakeMKV titles they identify, best first,
// and returns these numbers in the new order.
func rankDiscDBMatches(titles []TitleInfo, matches []DiscDBMatch) []int {
	type ranked struct {
		match DiscDBMatch
		named int
	}

	list := make([]ranked, len(matches))
	for i, m := range matches {
		list[i] = ranked{match: m, named: ApplyDiscDBMatch(slices.Clone(titles), m)}
	}
	sort.SliceStable(list, func(a, b int) bool { return list[a].named > list[b].named })

	scores := make([]int, len(list))
	for i, r := range list {
		matches[i] = r.match
		scores[i] = r.named
	}
	return scores
}

// searchDiscDBInteractive lets the user identify a disc that was not found by its hash in three
// steps: search and pick the movie or series, enter the season, enter the disc number.
func searchDiscDBInteractive(client *DiscDBClient, titles []TitleInfo) *DiscDBMatch {
	for {
		media, match := selectDiscDBMediaInteractive(client)
		if match != nil {
			return match
		}
		if media == nil {
			return nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), discDBRequestTimeout)
		discs, err := client.MediaDiscs(ctx, *media)
		cancel()

		if err != nil {
			fmt.Printf("\nTheDiscDB: %v\n\n", err)
			return nil
		}

		if len(discs) == 0 {
			fmt.Printf("\nTheDiscDB has no discs for %s.\n\n", media.String())
			continue
		}

		if match := selectDiscDBSeasonAndDisc(*media, discs, titles); match != nil {
			return match
		}
		fmt.Println()
	}
}

// discDBHashPattern matches a TheDiscDB disc ID or content hash (32 hex digits).
var discDBHashPattern = regexp.MustCompile(`^[0-9A-Fa-f]{32}$`)

// selectDiscDBMediaInteractive is step 1: search TheDiscDB and pick a movie, series or boxset.
// If the user enters a disc ID or disc hash instead, the disc itself is returned. Both are nil
// if the user leaves the search empty.
func selectDiscDBMediaInteractive(client *DiscDBClient) (*DiscDBMedia, *DiscDBMatch) {
	for {
		fmt.Printf("Enter a movie or series name or a disc ID to search TheDiscDB (leave empty to skip): ")
		term := readLine()
		if term == "" {
			return nil, nil
		}

		if discDBHashPattern.MatchString(term) {
			match, err := lookupDiscDBByID(client, term)
			switch {
			case err == nil:
				return nil, match
			case errors.Is(err, ErrDiscDBNoMatch):
				fmt.Printf("\nNo disc with ID or hash %s found.\n\n", strings.ToUpper(term))
				continue
			default:
				fmt.Printf("\nTheDiscDB: %v\n\n", err)
				return nil, nil
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), discDBRequestTimeout)
		media, err := client.SearchMedia(ctx, term)
		cancel()

		if err != nil {
			fmt.Printf("\nTheDiscDB: %v\n\n", err)
			return nil, nil
		}

		if len(media) == 0 {
			fmt.Printf("\nNo results for '%s'.\n\n", term)
			continue
		}

		var selected *DiscDBMedia
		if len(media) == 1 {
			selected = &media[0]
			fmt.Printf("\nFound: %s\n", selected.String())
		} else {
			fmt.Printf("\nSelect the movie or series:\n\n")
			fmt.Printf("0. None of these (search again)\n")
			for i, m := range media {
				fmt.Printf("%d. %s\n", i+1, m.String())
			}
			fmt.Println()

			n := readNumber("Selection: ", 0, len(media))
			if n == 0 {
				fmt.Println()
				continue
			}
			selected = &media[n-1]
		}

		return selected, nil
	}
}

// lookupDiscDBByID looks up a disc entered by the user. TheDiscDB shows two identifiers per
// disc, the "Disc ID" and the "Disc Hash" (content hash); both are 32 hex digits, so the disc ID
// is tried first and the content hash second.
func lookupDiscDBByID(client *DiscDBClient, id string) (*DiscDBMatch, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*discDBRequestTimeout)
	defer cancel()

	matches, idErr := client.LookupByDiscID(ctx, id)
	if idErr == nil {
		return &matches[0], nil
	}

	matches, err := client.LookupByHash(ctx, id)
	if err == nil {
		return &matches[0], nil
	}
	if errors.Is(err, ErrDiscDBNoMatch) && !errors.Is(idErr, ErrDiscDBNoMatch) {
		// The hash is unknown and the disc ID lookup failed: report why.
		return nil, idErr
	}
	return nil, err
}

// selectDiscDBSeasonAndDisc are steps 2 and 3: enter the season (skipped if the discs have at
// most one season) and the disc number (skipped if there is only one disc). If several releases
// have such a disc (e.g. DVD and Blu-ray), the user picks one; the disc that matches most
// titles of the inserted disc is listed first. It returns nil to go back to the search.
func selectDiscDBSeasonAndDisc(media DiscDBMedia, discs []DiscDBMatch, titles []TitleInfo) *DiscDBMatch {
	seasons := discDBSeasons(discs)

	for {
		season := -1
		switch {
		case len(seasons) == 1:
			season = seasons[0]
		case len(seasons) > 1:
			names := make([]string, len(seasons))
			for i, s := range seasons {
				names[i] = strconv.Itoa(s)
			}
			fmt.Printf("\nSeasons of %s in TheDiscDB: %s\n", media.Title, strings.Join(names, ", "))
			s, ok := readChoice("Season (leave empty to search again): ", seasons)
			if !ok {
				return nil
			}
			season = s
		}

		groups := discDBReleaseDiscs(discs, season)

		match, back := selectDiscDBDisc(media, discs, groups, season, titles)
		if match != nil {
			return match
		}
		if back && len(seasons) > 1 {
			continue
		}
		return nil
	}
}

// selectDiscDBDisc is step 3. It returns the chosen disc, or nil and whether the user asked to go
// back one step.
func selectDiscDBDisc(media DiscDBMedia, all []DiscDBMatch, groups [][]DiscDBMatch, season int, titles []TitleInfo) (*DiscDBMatch, bool) {
	maxDiscs := 0
	for _, g := range groups {
		maxDiscs = max(maxDiscs, len(g))
	}

	label := media.Title
	if season >= 0 {
		label = fmt.Sprintf("%s season %d", media.Title, season)
	}

	for {
		n := 1
		if maxDiscs > 1 {
			fmt.Printf("\n%s has %d discs in TheDiscDB.\n", label, maxDiscs)
			fmt.Printf("Disc number (l = list all discs, leave empty to go back): ")
			input := strings.ToLower(readLine())
			switch {
			case input == "":
				return nil, true
			case input == "l":
				if m := pickDiscDBMatch(slices.Clone(all), titles); m != nil {
					return m, false
				}
				continue
			default:
				v, err := strconv.Atoi(input)
				if err != nil || v < 1 || v > maxDiscs {
					fmt.Printf("Invalid disc number.\n")
					continue
				}
				n = v
			}
		}

		candidates := discDBDiscCandidates(groups, n)
		if len(candidates) == 1 {
			return &candidates[0], false
		}

		if m := pickDiscDBMatch(candidates, titles); m != nil {
			return m, false
		}
		if maxDiscs <= 1 {
			return nil, true
		}
	}
}

// pickDiscDBMatch lets the user choose one of the discs. It returns nil for "none of these".
func pickDiscDBMatch(matches []DiscDBMatch, titles []TitleInfo) *DiscDBMatch {
	scores := rankDiscDBMatches(titles, matches)

	fmt.Printf("\nSelect the disc:\n\n")
	fmt.Printf("0. None of these (go back)\n")
	for i, m := range matches {
		fmt.Printf("%d. %s (%d of %d titles recognized)\n", i+1, m.String(), scores[i], len(titles))
	}
	fmt.Println()

	n := readNumber("Selection: ", 0, len(matches))
	if n == 0 {
		return nil
	}
	return &matches[n-1]
}

// readNumber asks until the user enters a number between lo and hi. An empty input counts as 0
// ("none of these") if 0 is allowed.
func readNumber(prompt string, lo, hi int) int {
	for {
		fmt.Print(prompt)
		input := readLine()
		if input == "" && lo == 0 {
			return 0
		}
		n, err := strconv.Atoi(input)
		if err == nil && n >= lo && n <= hi {
			return n
		}
		fmt.Printf("Invalid selection.\n")
	}
}

// readChoice asks until the user enters one of the values. It returns false for an empty input.
func readChoice(prompt string, values []int) (int, bool) {
	for {
		fmt.Print(prompt)
		input := readLine()
		if input == "" {
			return 0, false
		}
		if n, err := strconv.Atoi(input); err == nil && slices.Contains(values, n) {
			return n, true
		}
		fmt.Printf("Invalid input.\n")
	}
}
