package models

import (
	"database/sql"
	"testing"
)

func TestNormalizeCommunityList(t *testing.T) {
	in := " Memes \n!News@Lemmy.World, https://lemmy.world/c/memes\nkbin.earth/m/retro\nmemes\n@bad\n https://x.example/post/1"
	want := "memes\nnews@lemmy.world\nmemes@lemmy.world\nretro@kbin.earth"
	if got := NormalizeCommunityList(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSourceLabelAndExcludes(t *testing.T) {
	s := &Source{InstanceURL: "https://lemmy.world", WatchType: WatchTypeInstance}
	if s.Label() != "lemmy.world" {
		t.Errorf("instance label %q", s.Label())
	}
	s.Name = sql.NullString{String: "World local", Valid: true}
	if s.Label() != "World local" {
		t.Errorf("named label %q", s.Label())
	}
	s.ExcludeCommunities = sql.NullString{String: "memes\nnews@lemmy.world", Valid: true}
	for c, want := range map[string]bool{"memes@lemmy.world": true, "Memes@kbin.earth": true,
		"news@lemmy.world": true, "news@other.example": false, "": false, "retro@lemmy.world": false} {
		if s.Excludes(c) != want {
			t.Errorf("Excludes(%q) = %v", c, !want)
		}
	}
	if s.ExcludeCount() != 2 {
		t.Errorf("count %d", s.ExcludeCount())
	}
}
