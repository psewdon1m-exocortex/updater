package selfupdate

import "testing"

func TestSelfUpdateUsesExactStableSelection(t *testing.T) {
	releases := []githubRelease{{TagName: "updater-v0.4.9"}, {TagName: "updater-v99.0.0", Prerelease: true}, {TagName: "v99.0.0"}, {TagName: "updater-v0.5.0-rc.1"}, {TagName: "updater-v0.5.0"}, {TagName: "updater-v98.0.0", Draft: true}}
	for _, item := range []struct{ requested, tag string }{{"", "updater-v0.5.0"}, {"0.4.9", "updater-v0.4.9"}, {"0.5.0", "updater-v0.5.0"}, {"0.5.0-rc.1", ""}, {"99.0.0", ""}, {"0.5.1", ""}} {
		selected := selectRelease(releases, item.requested)
		if item.tag == "" {
			if selected != nil {
				t.Fatalf("invalid candidate selected: %+v", selected)
			}
		} else if selected == nil || selected.TagName != item.tag {
			t.Fatalf("wrong exact release for %s: %+v", item.requested, selected)
		}
	}
}
