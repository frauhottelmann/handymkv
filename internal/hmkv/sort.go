package hmkv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SeriesSeasonDirectory is the folder an encoded episode is sorted into, relative to the
// HandBrake output directory: "<Series>/S01". It is empty if the title is not an episode with a
// known series and season.
func (t *TitleInfo) SeriesSeasonDirectory() string {
	if t.DiscDBSeries == "" || t.DiscDBSeason < 0 {
		return ""
	}

	series := sanitizeFileName(t.DiscDBSeries)
	if series == "" {
		return ""
	}

	return filepath.Join(series, fmt.Sprintf("S%02d", t.DiscDBSeason))
}

// sortedEpisode is one encoded episode that was moved into its season folder.
type sortedEpisode struct {
	from, to string
}

// sortSeriesEpisodes moves the encoded episodes from the run folder into
// <baseDir>/<Series>/S01, S02, ... and updates HandBrakeOutputPath of the entries accordingly.
// Titles that are not episodes (or whose season is unknown) are left where they are. Failures
// are reported but never stop the run. runDir is the folder of this run; it is removed if it is
// empty afterwards.
func sortSeriesEpisodes(baseDir, runDir string, titles []TitleInfo, entries []EncodingParams) []sortedEpisode {
	moved := make([]sortedEpisode, 0)
	sourceDirs := make(map[string]struct{})

	for i := range entries {
		entry := &entries[i]

		var title *TitleInfo
		for j := range titles {
			if titles[j].DiscId == entry.DiscId && titles[j].Index == entry.TitleIndex {
				title = &titles[j]
				break
			}
		}

		if title == nil {
			continue
		}

		rel := title.SeriesSeasonDirectory()
		if rel == "" {
			continue
		}

		src := entry.HandBrakeOutputPath
		if _, err := os.Stat(src); err != nil {
			// e.g. moved away by an automation
			fmt.Printf("Warning: %s was not sorted into its season folder - %v\n", filepath.Base(src), err)
			continue
		}

		targetDir := filepath.Join(baseDir, rel)
		if err := os.MkdirAll(targetDir, 0740); err != nil {
			fmt.Printf("Warning: could not create the season folder %s - %v\n", targetDir, err)
			continue
		}

		dst := uniqueFilePath(filepath.Join(targetDir, filepath.Base(src)))

		if err := os.Rename(src, dst); err != nil {
			fmt.Printf("Warning: could not move %s to %s - %v\n", src, dst, err)
			continue
		}

		sourceDirs[filepath.Dir(src)] = struct{}{}
		entry.HandBrakeOutputPath = dst
		moved = append(moved, sortedEpisode{from: src, to: dst})
	}

	// Remove the folders that are empty now (os.Remove does not delete non-empty folders).
	for dir := range sourceDirs {
		if isWithinDir(runDir, dir) {
			os.Remove(dir)
		}
	}
	if runDir != "" {
		os.Remove(runDir)
	}

	return moved
}

// uniqueFilePath returns path, or "name (2).ext", "name (3).ext", ... if path already exists, so
// an existing file is never overwritten.
func uniqueFilePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}

	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)

	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, n, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// isWithinDir reports whether path is dir or a folder below it.
func isWithinDir(dir, path string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// printSortedEpisodes tells the user where the sorted episodes went.
func printSortedEpisodes(baseDir string, moved []sortedEpisode) {
	if len(moved) == 0 {
		return
	}

	fmt.Printf("\nSorted %d episode(s) into season folders:\n", len(moved))

	for _, m := range moved {
		rel, err := filepath.Rel(baseDir, m.to)
		if err != nil {
			rel = m.to
		}
		fmt.Printf("  - %s\n", rel)
	}
}
