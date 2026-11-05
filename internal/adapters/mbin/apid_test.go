package mbin

import "testing"

func TestEntryApID(t *testing.T) {
	remote := "https://lemmy.world/post/52399701"
	if got := entryApID(&remote, "https://kbin.earth/m/x/t/1/slug", "mildlyinteresting@lemmy.world", 3229132); got != remote {
		t.Errorf("remote entry: %q", got)
	}
	if got := entryApID(nil, "https://kbin.earth/m/retro/t/55/some-slug", "retro", 55); got != "https://kbin.earth/m/retro/t/55" {
		t.Errorf("local entry: %q", got)
	}
	if got := entryApID(nil, "https://kbin.earth", "", 0); got != "" {
		t.Errorf("no info: %q", got)
	}
}
