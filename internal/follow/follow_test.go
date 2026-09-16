package follow

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/bspeelm/bivy/internal/media"
)

const (
	chanA = "UCaaaaaaaaaaaaaaaaaaaaaa"
	chanB = "UCbbbbbbbbbbbbbbbbbbbbbb"
)

func at(day int) time.Time { return time.Date(2026, 9, day, 12, 0, 0, 0, time.UTC) }

func TestAddFollowsAChannel(t *testing.T) {
	state, err := State{}.Add(Channel{ID: chanA, Title: "A"}, at(1))
	if err != nil {
		t.Fatal(err)
	}

	got, found := state.Find(chanA)
	if !found {
		t.Fatal("the channel was not followed")
	}
	if !got.FollowedAt.Equal(at(1)) {
		t.Errorf("FollowedAt = %s, want %s", got.FollowedAt, at(1))
	}
	// Nothing has happened since the moment of following, so nothing the feed
	// already carries is new.
	if !got.LastVisit.Equal(at(1)) {
		t.Errorf("LastVisit = %s, want the follow time", got.LastVisit)
	}
}

func TestAddRefusesSomethingThatIsNotAChannel(t *testing.T) {
	for _, bad := range []string{"", "nonsense", "--exec=touch /tmp/pwned"} {
		if _, err := (State{}).Add(Channel{ID: bad}, at(1)); err == nil {
			t.Errorf("Add(%q) returned no error", bad)
		}
	}
}

// Reported rather than silently ignored: "follow" printing success on a
// channel already followed reads as a subscription that was not made.
func TestAddRefusesADuplicate(t *testing.T) {
	state, err := State{}.Add(Channel{ID: chanA, Title: "A"}, at(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.Add(Channel{ID: chanA, Title: "A"}, at(2)); !errors.Is(err, ErrAlreadyFollowed) {
		t.Errorf("second Add returned %v, want ErrAlreadyFollowed", err)
	}
}

// The functions that change a State return a new one. A caller that keeps the
// old value keeps the old value.
func TestStateIsNotChangedInPlace(t *testing.T) {
	before, err := State{}.Add(Channel{ID: chanA, Title: "A"}, at(1))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := before.Add(Channel{ID: chanB, Title: "B"}, at(2)); err != nil {
		t.Fatal(err)
	}
	if got := len(before.Channels); got != 1 {
		t.Errorf("the original state now has %d channels; Add wrote through it", got)
	}

	if _, removed := before.Remove(chanA); !removed {
		t.Fatal("Remove reported nothing removed")
	}
	if got := len(before.Channels); got != 1 {
		t.Errorf("the original state now has %d channels; Remove wrote through it", got)
	}

	after := Visited(before, []media.Channel{{ID: chanA}}, at(9))
	if before.Channels[0].LastVisit.Equal(after.Channels[0].LastVisit) {
		t.Error("Visited did not change anything, so this test proves nothing")
	}
	if !before.Channels[0].LastVisit.Equal(at(1)) {
		t.Error("Visited wrote through the original state")
	}
}

func TestRemoveReportsWhetherItRemovedAnything(t *testing.T) {
	state, err := State{}.Add(Channel{ID: chanA}, at(1))
	if err != nil {
		t.Fatal(err)
	}

	if _, removed := state.Remove(chanB); removed {
		t.Error("removing a channel that was not followed reported success")
	}
	next, removed := state.Remove(chanA)
	if !removed {
		t.Error("removing a followed channel reported nothing removed")
	}
	if len(next.Channels) != 0 {
		t.Errorf("%d channels left, want 0", len(next.Channels))
	}
}

func video(id string, day int) media.Video {
	return media.Video{ID: id, Title: id, Published: at(day)}
}

func twoFollowed(t *testing.T) State {
	t.Helper()
	state, err := State{}.Add(Channel{ID: chanA, Title: "Aye"}, at(2))
	if err != nil {
		t.Fatal(err)
	}
	state, err = state.Add(Channel{ID: chanB, Title: "Bee"}, at(2))
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestDashboardMarksWhatArrivedSinceTheLastVisit(t *testing.T) {
	state := twoFollowed(t)

	rows := Dashboard(state, []media.Channel{
		{ID: chanA, Videos: []media.Video{video("aaaaaaaaaaa", 5), video("ccccccccccc", 1)}},
		{ID: chanB, Videos: []media.Video{video("bbbbbbbbbbb", 4)}},
	}, 0)

	if got, want := len(rows), 3; got != want {
		t.Fatalf("%d rows, want %d", got, want)
	}

	// Newest first, across channels rather than within them.
	for i, want := range []string{"aaaaaaaaaaa", "bbbbbbbbbbb", "ccccccccccc"} {
		if rows[i].Video.ID != want {
			t.Errorf("row %d is %q, want %q", i, rows[i].Video.ID, want)
		}
	}

	// Both channels were followed on day 2, so day 5 and day 4 are new and
	// day 1 is backlog.
	for i, want := range []bool{true, true, false} {
		if rows[i].New != want {
			t.Errorf("row %d New = %v, want %v", i, rows[i].New, want)
		}
	}
}

// The follow list decides what is on the dashboard, not what came back from a
// fetch. A response is allowed to contain more than was asked for.
func TestDashboardIgnoresChannelsThatAreNotFollowed(t *testing.T) {
	state, err := State{}.Add(Channel{ID: chanA, Title: "Aye"}, at(1))
	if err != nil {
		t.Fatal(err)
	}

	rows := Dashboard(state, []media.Channel{
		{ID: chanA, Videos: []media.Video{video("aaaaaaaaaaa", 3)}},
		{ID: chanB, Videos: []media.Video{video("bbbbbbbbbbb", 3)}},
	}, 0)

	if got, want := len(rows), 1; got != want {
		t.Fatalf("%d rows, want %d", got, want)
	}
	if rows[0].Video.ID != "aaaaaaaaaaa" {
		t.Errorf("row is %q, want the followed channel's entry", rows[0].Video.ID)
	}
}

// The name the user sees is the one on their follow list, not whatever a given
// entry claims its author is.
func TestDashboardUsesTheFollowedName(t *testing.T) {
	state := twoFollowed(t)

	rows := Dashboard(state, []media.Channel{{
		ID:    chanA,
		Title: "The feed's idea of the name",
		Videos: []media.Video{{
			ID: "aaaaaaaaaaa", Author: "Someone Else", Published: at(5),
		}},
	}}, 0)

	if got, want := rows[0].Channel, "Aye"; got != want {
		t.Errorf("channel shown as %q, want %q", got, want)
	}
}

func TestDashboardFallsBackToTheFeedName(t *testing.T) {
	state, err := State{}.Add(Channel{ID: chanA}, at(1))
	if err != nil {
		t.Fatal(err)
	}

	rows := Dashboard(state, []media.Channel{
		{ID: chanA, Title: "Learned From The Feed", Videos: []media.Video{video("aaaaaaaaaaa", 3)}},
	}, 0)

	if got, want := rows[0].Channel, "Learned From The Feed"; got != want {
		t.Errorf("channel shown as %q, want %q", got, want)
	}
}

func TestDashboardLimitsRows(t *testing.T) {
	state := twoFollowed(t)

	var videos []media.Video
	for i := range 10 {
		videos = append(videos, video("aaaaaaaaaa"+string(rune('a'+i)), 3+i))
	}
	rows := Dashboard(state, []media.Channel{{ID: chanA, Videos: videos}}, 4)

	if got, want := len(rows), 4; got != want {
		t.Fatalf("%d rows, want %d", got, want)
	}
	// The limit keeps the newest, not the first four that happened to arrive.
	if got, want := rows[0].Video.Published, at(12); !got.Equal(want) {
		t.Errorf("newest kept row is %s, want %s", got, want)
	}
}

// A network blip must not silently consume the thing the user launched bivy to
// see. Only what was actually fetched is marked visited.
func TestVisitedAdvancesOnlyTheChannelsThatWereFetched(t *testing.T) {
	state := twoFollowed(t)

	next := Visited(state, []media.Channel{{ID: chanA}}, at(9))

	a, _ := next.Find(chanA)
	b, _ := next.Find(chanB)
	if !a.LastVisit.Equal(at(9)) {
		t.Errorf("the fetched channel's LastVisit = %s, want %s", a.LastVisit, at(9))
	}
	if !b.LastVisit.Equal(at(2)) {
		t.Errorf("the unfetched channel's LastVisit = %s, want it unchanged", b.LastVisit)
	}
}

func TestRetitleLearnsNamesFromFeeds(t *testing.T) {
	state, err := State{}.Add(Channel{ID: chanA}, at(1))
	if err != nil {
		t.Fatal(err)
	}
	if state.Channels[0].Title != "" {
		t.Fatal("the fixture already has a title; this test proves nothing")
	}

	next := Retitle(state, []media.Channel{{ID: chanA, Title: "Its Real Name"}})
	if got, want := next.Channels[0].Title, "Its Real Name"; got != want {
		t.Errorf("title = %q, want %q", got, want)
	}

	// A feed that returns no title does not erase the name already known.
	kept := Retitle(next, []media.Channel{{ID: chanA, Title: ""}})
	if got, want := kept.Channels[0].Title, "Its Real Name"; got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
}

// The file on disk should not churn between saves, so a diff of it means
// something.
func TestTheFollowListKeepsAStableOrder(t *testing.T) {
	first, err := State{}.Add(Channel{ID: chanB, Title: "Zebra"}, at(1))
	if err != nil {
		t.Fatal(err)
	}
	first, err = first.Add(Channel{ID: chanA, Title: "Aardvark"}, at(2))
	if err != nil {
		t.Fatal(err)
	}

	second, err := State{}.Add(Channel{ID: chanA, Title: "Aardvark"}, at(2))
	if err != nil {
		t.Fatal(err)
	}
	second, err = second.Add(Channel{ID: chanB, Title: "Zebra"}, at(1))
	if err != nil {
		t.Fatal(err)
	}

	for i := range first.Channels {
		if first.Channels[i].ID != second.Channels[i].ID {
			t.Fatalf("the order depends on the order things were added: %v vs %v",
				first.Channels[i].ID, second.Channels[i].ID)
		}
	}
	if first.Channels[0].Title != "Aardvark" {
		t.Errorf("the list starts with %q, want it sorted by name", first.Channels[0].Title)
	}
}

// A mark made by hand has to be removable by hand, or the checkmark is a trap:
// one keystroke to set, nothing to undo.
func TestUnwatchForgetsAVideo(t *testing.T) {
	const id = "aaaaaaaaaaa"
	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)

	state := MarkWatched(State{}, id, now)
	if !state.HasWatched(id) {
		t.Fatal("the fixture did not mark it watched")
	}

	after := Unwatch(state, id)
	if after.HasWatched(id) {
		t.Error("the video is still watched after Unwatch")
	}
	if state.HasWatched(id) != true {
		t.Error("Unwatch changed the state it was given; these are values, not receivers")
	}
}

// Unwatching something that was never watched is a keypress on the wrong row,
// not an error worth a message.
func TestUnwatchingWhatWasNotWatchedChangesNothing(t *testing.T) {
	state := MarkWatched(State{}, "aaaaaaaaaaa", time.Now())

	after := Unwatch(state, "bbbbbbbbbbb")
	if len(after.Watched) != len(state.Watched) || !after.HasWatched("aaaaaaaaaaa") {
		t.Errorf("Unwatch of an unwatched video changed the history: %v", after.Watched)
	}
}

// Every call here that returns a changed State used to rebuild it field by
// field, naming the ones it carried. That makes a field added later a field
// six of them quietly drop, and nothing would have failed. Each call now
// copies the whole thing, and this is what says so: set every field, and check
// none of them vanishes through a call that does not own it.
func TestAChangedStateCarriesTheFieldsItDoesNotOwn(t *testing.T) {
	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	base := State{
		Channels: []Channel{{ID: "UCaaaaaaaaaaaaaaaaaaaaaa", Title: "Aye", FollowedAt: now, LastVisit: now}},
		Watched:  map[string]time.Time{"aaaaaaaaaaa": now},
	}

	for _, tc := range []struct {
		name  string
		owns  string
		apply func(State) State
	}{
		{"MarkWatched", "Watched", func(s State) State { return MarkWatched(s, "bbbbbbbbbbb", now) }},
		{"Unwatch", "Watched", func(s State) State { return Unwatch(s, "aaaaaaaaaaa") }},
		{"Add", "Channels", func(s State) State {
			next, err := s.Add(Channel{ID: "UCbbbbbbbbbbbbbbbbbbbbbb", Title: "Bee"}, now)
			if err != nil {
				t.Fatal(err)
			}
			return next
		}},
		{"Remove", "Channels", func(s State) State { next, _ := s.Remove("UCaaaaaaaaaaaaaaaaaaaaaa"); return next }},
		{"Visited", "Channels", func(s State) State { return Visited(s, nil, now) }},
		{"Retitle", "Channels", func(s State) State { return Retitle(s, nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := reflect.ValueOf(tc.apply(base))
			want := reflect.ValueOf(base)

			for i := range got.NumField() {
				name := got.Type().Field(i).Name
				if name == tc.owns {
					continue
				}
				if !reflect.DeepEqual(got.Field(i).Interface(), want.Field(i).Interface()) {
					t.Errorf("%s changed %s, which it does not own: %v became %v",
						tc.name, name, want.Field(i).Interface(), got.Field(i).Interface())
				}
			}
		})
	}
}

func aVideo(id, title string) media.Video {
	return media.Video{ID: id, Title: title, Author: "Someone", Duration: 5 * time.Minute}
}

func TestTheQueueKeepsWhatWasSavedInTheOrderItWasSaved(t *testing.T) {
	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)

	s := Save(State{}, aVideo("aaaaaaaaaaa", "First"), now)
	s = Save(s, aVideo("bbbbbbbbbbb", "Second"), now.Add(time.Minute))

	rows := QueueRows(s)
	if len(rows) != 2 {
		t.Fatalf("the queue holds %d rows, want 2", len(rows))
	}
	if rows[0].Video.Title != "First" || rows[1].Video.Title != "Second" {
		t.Errorf("the queue came back as %q then %q", rows[0].Video.Title, rows[1].Video.Title)
	}
	if !s.IsQueued("aaaaaaaaaaa") || s.IsQueued("ccccccccccc") {
		t.Error("IsQueued does not agree with what was saved")
	}
}

// Saving something twice must not move it. A list that reorders itself under
// the cursor is one nobody can keep a place in.
func TestSavingSomethingTwiceChangesNothing(t *testing.T) {
	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)

	s := Save(State{}, aVideo("aaaaaaaaaaa", "First"), now)
	s = Save(s, aVideo("bbbbbbbbbbb", "Second"), now)
	again := Save(s, aVideo("aaaaaaaaaaa", "First"), now.Add(time.Hour))

	if len(again.Queue) != 2 {
		t.Fatalf("saving a second time made %d rows", len(again.Queue))
	}
	if again.Queue[0].ID != "aaaaaaaaaaa" {
		t.Error("saving something already there moved it")
	}
}

func TestUnsaveDropsOnlyWhatWasNamed(t *testing.T) {
	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)

	s := Save(State{}, aVideo("aaaaaaaaaaa", "First"), now)
	s = Save(s, aVideo("bbbbbbbbbbb", "Second"), now)

	after := Unsave(s, "aaaaaaaaaaa")
	if len(after.Queue) != 1 || after.Queue[0].ID != "bbbbbbbbbbb" {
		t.Errorf("the queue is %v after dropping the first", after.Queue)
	}
	if len(s.Queue) != 2 {
		t.Error("Unsave changed the state it was given; these are values")
	}
	if len(Unsave(s, "ccccccccccc").Queue) != 2 {
		t.Error("dropping something that was never saved changed the queue")
	}
}

// The queue is what a row was when it was saved, not a pointer into a feed
// that has since rolled past it -- which is the case for a search result,
// where there is no feed to point into at all.
func TestAQueuedRowStandsOnItsOwn(t *testing.T) {
	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	s := Save(State{}, aVideo("aaaaaaaaaaa", "First"), now)

	row := QueueRows(s)[0]
	if row.Video.Title != "First" || row.Channel != "Someone" {
		t.Errorf("the row lost what it was saved with: %+v", row)
	}
	if row.Video.Duration != 5*time.Minute {
		t.Errorf("the row lost its duration: %v", row.Video.Duration)
	}
	if row.Video.Thumbnail == "" {
		t.Error("the row has no picture to draw")
	}
}

// Watching something does not empty the queue by itself: the tick shows on the
// row, and what removes it is the key that says so.
func TestAWatchedQueueRowStillShowsAsQueued(t *testing.T) {
	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	s := Save(State{}, aVideo("aaaaaaaaaaa", "First"), now)
	s = MarkWatched(s, "aaaaaaaaaaa", now)

	rows := QueueRows(s)
	if len(rows) != 1 {
		t.Fatalf("the queue holds %d rows, want 1", len(rows))
	}
	if !rows[0].Watched {
		t.Error("a watched row in the queue carries no tick")
	}
}
