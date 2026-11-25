package hmkv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyDiscDBMatchSetsSeriesAndSeason(t *testing.T) {
	titles := []TitleInfo{
		{Index: 0, FileName: "title_t00.mkv", SourceFile: "00800.mpls", SegmentMap: "3,4", FileSizeBytes: 3000},
		{Index: 3, FileName: "title_t03.mkv", SourceFile: "00900.mpls", FileSizeBytes: 500},
		{Index: 4, FileName: "title_t04.mkv", SourceFile: "00901.mpls", FileSizeBytes: 400},
	}

	ApplyDiscDBMatch(titles, sampleMatch())

	if got, want := titles[0].SeriesSeasonDirectory(), filepath.Join("Star Trek - The Next Generation", "S04"); got != want {
		t.Errorf("episode: SeriesSeasonDirectory() = %q, want %q", got, want)
	}
	if got := titles[1].SeriesSeasonDirectory(); got != "" {
		t.Errorf("extra: SeriesSeasonDirectory() = %q, want empty", got)
	}
	if got := titles[2].SeriesSeasonDirectory(); got != "" {
		t.Errorf("unknown title: SeriesSeasonDirectory() = %q, want empty", got)
	}
}

func TestApplyDiscDBMatchSeasonFallsBackToDisc(t *testing.T) {
	match := DiscDBMatch{
		MediaTitle:   "Some Show",
		MediaType:    "Series",
		ReleaseTitle: "Season 2",
		Disc: discDBDisc{Titles: []discDBTitle{
			{Index: 0, SourceFile: "00001.mpls", Item: &discDBItem{Title: "Pilot", Type: "Episode", Episode: "1"}},
		}},
	}
	titles := []TitleInfo{{Index: 0, FileName: "t.mkv", SourceFile: "00001.mpls"}}

	ApplyDiscDBMatch(titles, match)

	if got, want := titles[0].SeriesSeasonDirectory(), filepath.Join("Some Show", "S02"); got != want {
		t.Errorf("SeriesSeasonDirectory() = %q, want %q", got, want)
	}
}

func TestSortSeriesEpisodes(t *testing.T) {
	base := t.TempDir()
	runDir := filepath.Join(base, "handymkv_2026-01-01_00-00-00")
	discDir := filepath.Join(runDir, "DISC")

	ep1 := filepath.Join(discDir, "Show_-_S01E01_-_Pilot.mkv")
	ep2 := filepath.Join(discDir, "Show_-_S01E02_-_Second.mkv")
	movie := filepath.Join(discDir, "Movie.mkv")
	for _, p := range []string{ep1, ep2, movie} {
		writeSizedFile(t, p, 1)
	}

	// A file of an earlier run with the same name must not be overwritten.
	existing := filepath.Join(base, "Show", "S01", "Show_-_S01E02_-_Second.mkv")
	writeSizedFile(t, existing, 7)

	titles := []TitleInfo{
		{DiscId: 0, Index: 0, DiscDBSeries: "Show", DiscDBSeason: 1},
		{DiscId: 0, Index: 1, DiscDBSeries: "Show", DiscDBSeason: 1},
		{DiscId: 0, Index: 2, DiscDBType: "MainMovie"},
	}
	entries := []EncodingParams{
		{DiscId: 0, TitleIndex: 0, HandBrakeOutputPath: ep1},
		{DiscId: 0, TitleIndex: 1, HandBrakeOutputPath: ep2},
		{DiscId: 0, TitleIndex: 2, HandBrakeOutputPath: movie},
	}

	moved := sortSeriesEpisodes(base, runDir, titles, entries)
	if len(moved) != 2 {
		t.Fatalf("moved %d files, want 2", len(moved))
	}

	want := []string{
		filepath.Join(base, "Show", "S01", "Show_-_S01E01_-_Pilot.mkv"),
		filepath.Join(base, "Show", "S01", "Show_-_S01E02_-_Second (2).mkv"),
		movie,
	}
	for i, w := range want {
		if entries[i].HandBrakeOutputPath != w {
			t.Errorf("entry %d: path = %q, want %q", i, entries[i].HandBrakeOutputPath, w)
		}
		if _, err := os.Stat(w); err != nil {
			t.Errorf("entry %d: %v", i, err)
		}
	}

	if st, err := os.Stat(existing); err != nil || st.Size() != 7 {
		t.Errorf("existing file was changed: %v", err)
	}

	// The run folder still holds the movie and must be kept.
	if _, err := os.Stat(discDir); err != nil {
		t.Errorf("run folder with remaining files was removed: %v", err)
	}
}

func TestSortSeriesEpisodesRemovesEmptyRunFolder(t *testing.T) {
	base := t.TempDir()
	runDir := filepath.Join(base, "handymkv_run")
	ep := filepath.Join(runDir, "DISC", "ep.mkv")
	writeSizedFile(t, ep, 1)

	titles := []TitleInfo{{Index: 0, DiscDBSeries: "Show: The Series", DiscDBSeason: 0}}
	entries := []EncodingParams{{TitleIndex: 0, HandBrakeOutputPath: ep}}

	sortSeriesEpisodes(base, runDir, titles, entries)

	if want := filepath.Join(base, "Show - The Series", "S00", "ep.mkv"); entries[0].HandBrakeOutputPath != want {
		t.Errorf("path = %q, want %q", entries[0].HandBrakeOutputPath, want)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Errorf("empty run folder was not removed (err = %v)", err)
	}
}
